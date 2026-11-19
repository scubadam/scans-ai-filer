package ai

import (
	"context"
	"fmt"

	"github.com/scubadam/scans-ai-filer/internal/taxonomy"
)

// DocumentInput holds the document bytes and hints passed to an AI provider.
type DocumentInput struct {
	Bytes            []byte
	MimeType         string
	OriginalFilename string
	FallbackExt      string
	FallbackDate     string
	Taxonomy         []taxonomy.Rule
}

// FilingMetadata is the structured, PII-sanitized classification returned by an AI provider.
type FilingMetadata struct {
	DocumentDate        string `json:"document_date"`
	Organization        string `json:"organization"`
	DocumentType        string `json:"document_type"`
	Category            string `json:"category"`
	Subcategory         string `json:"subcategory"`
	Summary             string `json:"summary"`
	SuggestedFilename   string `json:"suggested_filename"`
	SuggestedFolderPath string `json:"suggested_folder_path"`
	IsTaxonomyMatch     bool   `json:"is_taxonomy_match"`
}

// Provider abstracts document classification across cloud or local vision/LLM backends.
type Provider interface {
	Name() string
	AnalyzeDocument(ctx context.Context, input DocumentInput) (*FilingMetadata, error)
}

// BuildClassificationPrompt generates the shared taxonomy-guided, PII-sanitized prompt.
func BuildClassificationPrompt(input DocumentInput) string {
	taxonomyText := taxonomy.FormatForPrompt(input.Taxonomy)
	return fmt.Sprintf(`You are an expert document archivist and personal filing assistant.
Analyze this scanned document and extract structured, privacy-safe metadata for filing and categorization according to the provided TAXONOMY CATALOG.

%s

PRIVACY & SANITIZATION RULES (CRITICAL):
- The extracted metadata will be stored in plaintext file names, folder paths, and cloud drive search properties.
- NEVER include sensitive PII or identifiers in summary, suggested_filename, or any output field.
- Specifically EXCLUDE: account numbers, sort codes, IBANs, card numbers, policy/reference/claim numbers, tax/NI/SSN/NHS/passport IDs, monetary amounts or balances, street addresses, phone numbers, email addresses, dates of birth, or specific medical diagnoses/conditions.
- Keep "summary" strictly generic and innocuous (2 to 5 words, e.g. "Annual Policy Renewal", "Monthly Statement", "Service Invoice", "Appointment Letter").

TAXONOMY MATCHING INSTRUCTIONS:
1. Examine the document to identify the issuer, sender, subject matter, and keywords.
2. Check the TAXONOMY CATALOG above:
   - If the sender, brand, or topic matches an entry in the catalog (via keywords or organization name), you MUST use that exact Category, Subcategory, and Organization.
   - For suggested_folder_path, substitute {year} in that entry's Path template with the 4-digit year of document_date. If the template has no {year}, use the path template directly.
   - Set is_taxonomy_match to true.
3. If NO catalog entry matches:
   - Set is_taxonomy_match to false.
   - Assign a sensible Category (e.g. Finance, Housing, Utilities, Education, Employment, Vehicle, Personal) and clean Organization.
   - Set suggested_folder_path to: "[Category]/[Organization]/[YYYY]" or "[Category]/[Subcategory]/[Organization]/[YYYY]".

Determine:
1. document_date: YYYY-MM-DD format (the explicit date of the invoice, letter, statement, receipt, or document creation).
   - If only month/year is present for the document's issue date, use YYYY-MM-01.
   - Do NOT use dates of birth, anniversaries, historical references, or individual entries inside tables/lists as the document_date.
   - If the document does not have a clear issue/creation date with high certainty, use the scan/upload date: %s.
2. organization: The canonical company or institution name.
3. document_type: Standard classification (e.g. Invoice, Bill, Bank Statement, Tax Return, Insurance Policy, Medical Letter, Contract, Receipt, Letter).
4. category: High-level category.
5. subcategory: High-level subcategory.
6. summary: 2 to 5 word generic, PII-free summary of the topic.
7. suggested_filename: Standardized filename in format:
   "YYYY-MM-DD - [Organization] - [Document Type] - [Summary]%s"
8. suggested_folder_path: Target directory path derived from the matched template or fallback rule.
9. is_taxonomy_match: Boolean indicating whether an existing taxonomy entry was matched.
`, taxonomyText, input.FallbackDate, input.FallbackExt)
}
