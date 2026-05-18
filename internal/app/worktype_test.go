package app

import "testing"

func TestClassifyCard(t *testing.T) {
	cases := []struct {
		name                            string
		input                           string
		wantType                        WorkType
		wantKind                        GitHubItemKind
		wantOwner, wantRepo, wantNumber string
	}{
		{
			name:      "AVM module issue",
			input:     "https://github.com/Azure/terraform-azurerm-avm-res-compute-diskencryptionset/issues/45",
			wantType:  WorkTypeAVMModule,
			wantKind:  GitHubItemKindIssue,
			wantOwner: "Azure", wantRepo: "terraform-azurerm-avm-res-compute-diskencryptionset", wantNumber: "45",
		},
		{
			name:      "AVM module pull request",
			input:     "see PR https://github.com/Azure/terraform-azurerm-avm-res-network-virtualnetwork/pull/127 for context",
			wantType:  WorkTypeAVMModule,
			wantKind:  GitHubItemKindPR,
			wantOwner: "Azure", wantRepo: "terraform-azurerm-avm-res-network-virtualnetwork", wantNumber: "127",
		},
		{
			name:      "terraform-provider-azurerm trumps Azure rule",
			input:     "https://github.com/hashicorp/terraform-provider-azurerm/issues/12345",
			wantType:  WorkTypeProviderAzureRM,
			wantKind:  GitHubItemKindIssue,
			wantOwner: "hashicorp", wantRepo: "terraform-provider-azurerm", wantNumber: "12345",
		},
		{
			name:      "Azure-owned other provider",
			input:     "https://github.com/Azure/terraform-provider-azapi/pull/9",
			wantType:  WorkTypeAzureProvider,
			wantKind:  GitHubItemKindPR,
			wantOwner: "Azure", wantRepo: "terraform-provider-azapi", wantNumber: "9",
		},
		{
			name:      "Azure terraform legacy module (not AVM, not provider)",
			input:     "https://github.com/Azure/terraform-azurerm-vnet/issues/3",
			wantType:  WorkTypeTerraformLegacy,
			wantKind:  GitHubItemKindIssue,
			wantOwner: "Azure", wantRepo: "terraform-azurerm-vnet", wantNumber: "3",
		},
		{
			name:      "non-Azure non-provider repo falls through to generic",
			input:     "https://github.com/lonegunmanb/some-other-tool/issues/1",
			wantType:  WorkTypeGeneric,
			wantKind:  GitHubItemKindIssue,
			wantOwner: "lonegunmanb", wantRepo: "some-other-tool", wantNumber: "1",
		},
		{
			name:     "no GitHub URL at all",
			input:    "Investigate the flaky build on Friday",
			wantType: WorkTypeGeneric,
			wantKind: GitHubItemKindUnknown,
		},
		{
			name:     "empty firstLine",
			input:    "",
			wantType: WorkTypeGeneric,
			wantKind: GitHubItemKindUnknown,
		},
		{
			name:      "URL is case-sensitive on owner but classification is case-insensitive",
			input:     "https://github.com/azure/Terraform-AzureRM-AVM-Res-Group/pull/1",
			wantType:  WorkTypeAVMModule,
			wantKind:  GitHubItemKindPR,
			wantOwner: "azure", wantRepo: "Terraform-AzureRM-AVM-Res-Group", wantNumber: "1",
		},
		{
			name:      "URL embedded with trailing punctuation does not break number capture",
			input:     "ref: https://github.com/Azure/terraform-azurerm-avm-res-foo/issues/42.",
			wantType:  WorkTypeAVMModule,
			wantKind:  GitHubItemKindIssue,
			wantOwner: "Azure", wantRepo: "terraform-azurerm-avm-res-foo", wantNumber: "42",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyCard(tc.input)
			if got.WorkType != tc.wantType {
				t.Errorf("WorkType = %q, want %q", got.WorkType, tc.wantType)
			}
			if got.GitHub.ItemKind != tc.wantKind {
				t.Errorf("GitHub.ItemKind = %q, want %q", got.GitHub.ItemKind, tc.wantKind)
			}
			if got.GitHub.Owner != tc.wantOwner {
				t.Errorf("GitHub.Owner = %q, want %q", got.GitHub.Owner, tc.wantOwner)
			}
			if got.GitHub.Repo != tc.wantRepo {
				t.Errorf("GitHub.Repo = %q, want %q", got.GitHub.Repo, tc.wantRepo)
			}
			if got.GitHub.Number != tc.wantNumber {
				t.Errorf("GitHub.Number = %q, want %q", got.GitHub.Number, tc.wantNumber)
			}
		})
	}
}
