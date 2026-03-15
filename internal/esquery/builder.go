package esquery

// BoolQuery builds an Elasticsearch bool query clause-by-clause,
// mirroring the PHP PhabricatorElasticsearchQueryBuilder.
type BoolQuery struct {
	Must    []any `json:"must,omitempty"`
	Should  []any `json:"should,omitempty"`
	Filter  []any `json:"filter,omitempty"`
	MustNot []any `json:"must_not,omitempty"`
}

func (q *BoolQuery) AddMust(clause any)    { q.Must = append(q.Must, clause) }
func (q *BoolQuery) AddShould(clause any)  { q.Should = append(q.Should, clause) }
func (q *BoolQuery) AddFilter(clause any)  { q.Filter = append(q.Filter, clause) }
func (q *BoolQuery) AddMustNot(clause any) { q.MustNot = append(q.MustNot, clause) }

func (q *BoolQuery) AddExists(field string) {
	q.AddFilter(map[string]any{
		"exists": map[string]any{"field": field},
	})
}

func (q *BoolQuery) AddTerms(field string, values []string) {
	q.AddFilter(map[string]any{
		"terms": map[string]any{field: values},
	})
}

func (q *BoolQuery) MustCount() int { return len(q.Must) }

// Phorge document field type constants (from PhabricatorSearchDocumentFieldType)
const (
	FieldTitle   = "titl"
	FieldBody    = "body"
	FieldComment = "cmnt"
	FieldAll     = "full"
	FieldCore    = "core"
)

// Phorge relationship constants (from PhabricatorSearchRelationship)
const (
	RelAuthor     = "auth"
	RelBook       = "book"
	RelReviewer   = "revw"
	RelSubscriber = "subs"
	RelCommenter  = "comm"
	RelOwner      = "ownr"
	RelProject    = "proj"
	RelRepository = "repo"

	RelOpen    = "open"
	RelClosed  = "clos"
	RelUnowned = "unow"
)

// AllFields returns all indexable document field constants.
func AllFields() []string {
	return []string{FieldTitle, FieldBody, FieldComment, FieldAll, FieldCore}
}

// AllRelationships returns all relationship constants.
func AllRelationships() []string {
	return []string{
		RelAuthor, RelBook, RelReviewer, RelSubscriber,
		RelCommenter, RelOwner, RelProject, RelRepository,
		RelOpen, RelClosed, RelUnowned,
	}
}
