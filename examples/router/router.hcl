# =============================================================================
# router.hcl — sample router configuration
# -----------------------------------------------------------------------------
# This file shows what `--router-dir/router.hcl` contains for the HCL-based
# router. The routing + classification logic lives in:
#
#   internal/app/router/       (route engine, rule engine, github_issue)
#
# The playbook loader / `{{<basename>}}` template renderer THIS FILE relies
# on (for `prompts = [...]`) is implemented. See:
#
#   internal/app/prompttmpl/   (Renderer)
#
# Reading order top-to-bottom:
#   1. kanban {}            — list-name knobs
#   2. route {} blocks      — what to do with each Trello webhook event
#   3. rule {} blocks       — which playbooks to load for the matched card
#
# Match semantics (the only thing you need to know):
#   - route / rule blocks are tried top-down.
#   - The first block whose `when` evaluates to true wins.
#   - Anything below it is ignored.
# =============================================================================


# =============================================================================
# 1. Kanban shape  (issue #5)
# -----------------------------------------------------------------------------
# Two-level taxonomy:
#
#   ROLE (纲)         — semantic meaning a single Trello list plays in the
#                       workflow. Stable name; the actual Trello list name
#                       is data. WORKER.md prompts reference roles, not names.
#
#   CATEGORY (门)     — routing-level kind. Derived from role by the engine,
#                       not configurable. Mapping:
#                         plan                  -> plan
#                         action                -> action
#                         wait.plan_review      -> wait
#                         wait.action_review    -> wait
#                         wait.generic          -> wait
#                         wait.exception        -> wait
#                         done                  -> done
#
# Why this matters: a worker prompt can say "move the card to the
# wait.plan_review list" instead of hard-coding "Ready for plan review".
# Renaming a column on the Trello board only requires editing this file.
#
# A well-formed kanban {} has exactly one block per role. List names across
# all roles must be unique (the engine validates this at load time).
# =============================================================================
kanban {

  # ----- the two "agent is doing something" roles ------------------------------
  plan {
    name = "Analyze"
  }
  action {
    name = "In action"
  }

  # ----- the wait gate (4 sub-roles, all routed identically) -------------------
  # WORKER.md uses each sub-role independently to reason about WHY the card is
  # waiting. Routing collapses all of them to category=wait.
  wait {
    plan_review {
      name = "Ready for plan review"
    }
    action_review {
      name = "Ready for review"
    }
    generic {
      name = "Pending PR"
    }
    exception {
      name = "Need Attention"
    }
  }

  # ----- terminal -------------------------------------------------------------
  done {
    name = "Done"
  }

  # A comment whose trimmed text starts with ANY of these prefixes is treated
  # as agent-authored and dropped (so the agent does not loop on its own
  # comments). Add new prefixes here when introducing a new agent identity.
  agent_comment_prefixes = ["[agent]:"]
}


# =============================================================================
# Engine-derived views over kanban {}, available to route/rule `when`:
#
#   kanban.plan_lists                   list(string)  ["Analyze"]
#   kanban.action_lists                 list(string)  ["In action"]
#   kanban.wait_lists                   list(string)  union of all wait sub-roles
#   kanban.done_lists                   list(string)  ["Done"]
#
# Per-role accessors for prompts / fine-grained rules:
#   kanban.plan.name                    string        "Analyze"
#   kanban.action.name                  string        "In action"
#   kanban.wait.plan_review.name        string        "Ready for plan review"
#   kanban.wait.action_review.name      string        "Ready for review"
#   kanban.wait.generic.name            string        "Pending PR"
#   kanban.wait.exception.name          string        "Need Attention"
#   kanban.done.name                    string        "Done"
#
# All comparisons should use lower(...) on both sides; the engine normalises
# the kanban list names to lowercase at load time.
# =============================================================================


# =============================================================================
# 2. Event routing  (replaces routing.go's Route())
# -----------------------------------------------------------------------------
# Variables visible in `when`:
#   action.type        ∈ "updateCard" | "createCard" | "commentCard"
#                        | "deleteCard" | "deleteComment" | <other>
#   action.card_id     string; "" when the event has no card id
#   action.list_after  string; non-empty only for an updateCard list move
#   action.list_name   string; createCard's destination list (data.list.name)
#   action.comment     string; commentCard's text (data.text)
#
# `do` field values:
#   "drop"             ignore the event
#   "dispatch"         hand to the per-card worker (spawn one if needed)
#   "terminate"        tell the worker to clean up and exit
#   "notify_departure" tell the worker the card left active flow; do not spawn
#
# `reason` is logged verbatim and shows up in the gateway log line, so it's
# useful for after-the-fact debugging: "why did this event get dropped?"
# =============================================================================

route "no_card_id" {
  when   = action.card_id == ""
  do     = "drop"
  reason = "no_card_id"
}

# ---- updateCard (a card was edited or moved) --------------------------------
route "updateCard_no_list_move" {
  when   = action.type == "updateCard" && action.list_after == ""
  do     = "drop"
  reason = "updateCard_no_list_move"
}

route "moved_to_done" {
  when   = (action.type == "updateCard"
        && contains(kanban.done_lists, lower(action.list_after)))
  do     = "terminate"
  reason = "moved_to_done"
}

route "moved_to_plan_list" {
  when   = (action.type == "updateCard"
        && contains(kanban.plan_lists, lower(action.list_after)))
  do     = "dispatch"
  reason = "moved_to_plan_list"
}

route "moved_to_action_list" {
  when   = (action.type == "updateCard"
        && contains(kanban.action_lists, lower(action.list_after)))
  do     = "dispatch"
  reason = "moved_to_action_list"
}

route "moved_to_wait_list" {
  when   = (action.type == "updateCard"
        && contains(kanban.wait_lists, lower(action.list_after)))
  do     = "notify_departure"
  reason = "moved_to_wait_list"
}

# Reached only when the card was moved INTO a list that is not registered
# under any role in kanban {}. This is a kanban-config bug — every list on
# the board should be claimed by exactly one role. Drop with a loud reason
# so the operator notices in the log.
route "moved_to_unknown_list" {
  when   = action.type == "updateCard" && action.list_after != ""
  do     = "drop"
  reason = "moved_to_unknown_list"
}

# ---- createCard (a new card was added) --------------------------------------
# Created in plan or action role -> dispatch immediately.
# Created in wait/done/unknown -> drop (humans can move it later).
route "created_in_plan_list" {
  when   = (action.type == "createCard"
        && contains(kanban.plan_lists, lower(action.list_name)))
  do     = "dispatch"
  reason = "created_in_plan_list"
}

route "created_in_action_list" {
  when   = (action.type == "createCard"
        && contains(kanban.action_lists, lower(action.list_name)))
  do     = "dispatch"
  reason = "created_in_action_list"
}

route "created_in_non_active_list" {
  when   = action.type == "createCard"
  do     = "drop"
  reason = "created_in_non_active_list"
}

# ---- commentCard ------------------------------------------------------------
# Note: comments dispatch to the worker regardless of which list the card is
# currently in. A worker in a wait_for_* role reads the comment in place and
# does NOT move the card — that "stay put on comments" behaviour is enforced
# by WORKER.md, not by the router.
route "agent_self_comment" {
  when   = (action.type == "commentCard"
        && anytrue([for p in kanban.agent_comment_prefixes :
                      startswith(trimspace(action.comment), p)]))
  do     = "drop"
  reason = "agent_self_comment"
}

route "human_comment" {
  when   = action.type == "commentCard"
  do     = "dispatch"
  reason = "human_comment"
}

# ---- terminal / unsupported -------------------------------------------------
route "card_deleted" {
  when   = action.type == "deleteCard"
  do     = "terminate"
  reason = "card_deleted"
}

route "deleteComment_not_handled" {
  when   = action.type == "deleteComment"
  do     = "drop"
  reason = "deleteComment_not_handled"
}

route "unsupported_action_type" {
  when   = true
  do     = "drop"
  reason = "unsupported_action_type"
}


# =============================================================================
# 3. Card classification + prompt selection
#    (replaces the former worktype.go classification)
# -----------------------------------------------------------------------------
# Variables visible in `when`:
#   card.id         string
#   card.name       string                — Trello card title
#   card.list_name  string                — current Trello list name
#   card.first_line string                — first non-empty line of the description
#   card.labels     list(string)          — Trello label names
#
# Helper functions visible in `when`:
#   github_issue(s) — parse a GitHub issue/PR URL out of `s`. Returns either
#                     null (no GitHub URL found) or an object:
#                       {
#                         owner  = "..."
#                         repo   = "..."
#                         number = "..."
#                         kind   = "issue" | "pr"
#                         url    = "..."
#                       }
#                     Always check `!= null` before accessing fields.
#
#   ado_workitem(s) — same idea, ADO work item URL. Not active today.
#                     Reserved name to show how new providers get added.
#
# Plus the standard hclfuncs library: lower / upper / startswith / endswith /
# strcontains / regex / trimspace / contains / anytrue / alltrue / try / can /
# coalesce / ... see github.com/lonegunmanb/hclfuncs.
#
# Match semantics: top-down, first when==true wins. After a match, every name
# in `prompts` is resolved against the playbooks directory (`--playbooks-dir`,
# the only location the engine reads .md files from), goes through the same
# pre-render pass that handles `{{<basename>}}` cross-references, and is
# appended to the worker's system prompt under a section header
# `## RULE PLAYBOOK — <name>`. Bare basenames only — path separators
# (`/` or `\`) and `..` are rejected at load time. `prompts = []` means: this
# rule matched, but contributes no playbook (worker falls back to the embedded
# WORKER.md §0 self-bootstrap).
# =============================================================================

# --- terraform-provider-azurerm
rule "azurerm_provider_issue" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).repo) == "terraform-provider-azurerm"
      && github_issue(card.first_line).kind == "issue")
  prompts = ["azurerm_provider_issue.md"]
}

rule "azurerm_provider_pr" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).repo) == "terraform-provider-azurerm"
      && github_issue(card.first_line).kind == "pr")
  prompts = ["azurerm_provider_pr.md"]
}

# --- AVM module
rule "avm_issue" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).owner) == "azure"
      && strcontains(lower(github_issue(card.first_line).repo), "terraform")
      && strcontains(lower(github_issue(card.first_line).repo), "avm")
      && github_issue(card.first_line).kind == "issue")
  prompts = ["avm_issue.md"]
}

rule "avm_pr" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).owner) == "azure"
      && strcontains(lower(github_issue(card.first_line).repo), "terraform")
      && strcontains(lower(github_issue(card.first_line).repo), "avm")
      && github_issue(card.first_line).kind == "pr")
  prompts = ["avm_pr.md"]
}

# --- other Azure/terraform-provider-*  (no per-kind playbook)
rule "azure_other_provider" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).owner) == "azure"
      && startswith(lower(github_issue(card.first_line).repo), "terraform-provider-"))
  prompts = []
}

# --- terraform-legacy-module
# Order matters: the AVM and provider-* rules above already absorbed those
# repos, so this rule does NOT need to re-exclude them.
rule "tfvm_issue" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).owner) == "azure"
      && strcontains(lower(github_issue(card.first_line).repo), "terraform")
      && github_issue(card.first_line).kind == "issue")
  prompts = ["tfvm_issue.md"]
}

rule "tfvm_pr" {
  when = (github_issue(card.first_line) != null
      && lower(github_issue(card.first_line).owner) == "azure"
      && strcontains(lower(github_issue(card.first_line).repo), "terraform")
      && github_issue(card.first_line).kind == "pr")
  prompts = ["tfvm_pr.md"]
}

# --- generic fallback (matches anything; nothing extra to load)
rule "fallback" {
  when    = true
  prompts = []
}
