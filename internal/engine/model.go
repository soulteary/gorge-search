package engine

// Document represents a PhabricatorSearchAbstractDocument for indexing.
type Document struct {
	PHID          string             `json:"phid"`
	Type          string             `json:"type"`
	Title         string             `json:"title"`
	DateCreated   int64              `json:"dateCreated"`
	DateModified  int64              `json:"dateModified"`
	Fields        []DocumentField    `json:"fields,omitempty"`
	Relationships []DocumentRelation `json:"relationships,omitempty"`
}

// DocumentField corresponds to a (field_name, corpus, aux) tuple
// from PhabricatorSearchAbstractDocument::getFieldData().
type DocumentField struct {
	Name   string `json:"name"`
	Corpus string `json:"corpus"`
	Aux    string `json:"aux,omitempty"`
}

// DocumentRelation corresponds to a (field_name, related_phid, rtype, time) tuple
// from PhabricatorSearchAbstractDocument::getRelationshipData().
type DocumentRelation struct {
	Name        string `json:"name"`
	RelatedPHID string `json:"relatedPHID"`
	RType       string `json:"rtype"`
	Timestamp   int64  `json:"timestamp,omitempty"`
}

// SearchQuery mirrors the parameters of a PhabricatorSavedQuery
// used in fulltext search.
type SearchQuery struct {
	Query           string   `json:"query"`
	Types           []string `json:"types,omitempty"`
	AuthorPHIDs     []string `json:"authorPHIDs,omitempty"`
	OwnerPHIDs      []string `json:"ownerPHIDs,omitempty"`
	SubscriberPHIDs []string `json:"subscriberPHIDs,omitempty"`
	ProjectPHIDs    []string `json:"projectPHIDs,omitempty"`
	RepositoryPHIDs []string `json:"repositoryPHIDs,omitempty"`
	Statuses        []string `json:"statuses,omitempty"`
	WithAnyOwner    bool     `json:"withAnyOwner,omitempty"`
	WithUnowned     bool     `json:"withUnowned,omitempty"`
	Exclude         string   `json:"exclude,omitempty"`
	Offset          int      `json:"offset,omitempty"`
	Limit           int      `json:"limit,omitempty"`
}
