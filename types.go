package osmmini

// Tags represents OSM tags as a simple string map.
type Tags map[string]string

// Node represents a node with coordinates and tags.
type Node struct {
	ID   int64
	Lat  float64
	Lon  float64
	Tags Tags
}

// Way represents a way with node references and tags.
type Way struct {
	ID      int64
	NodeIDs []int64
	Tags    Tags
}

// MemberType for relation members.
type MemberType int

const (
	MemberNode MemberType = iota
	MemberWay
	MemberRelation
)

// Member represents a relation member.
type Member struct {
	Type MemberType
	ID   int64
	Role string
}

// Relation represents an OSM relation with members and tags.
type Relation struct {
	ID      int64
	Members []Member
	Tags    Tags
}

// Options configures Extract behaviour.
type Options struct {
	KeepTag             func(string) bool
	EmitWayNodeIDs      bool
	EmitRelationMembers bool
}

// Callbacks passed to Extract to receive parsed entities.
type Callbacks struct {
	Node            func(id int64, lat, lon float64) error
	AddressNode     func(n Node) error
	HighwayWay      func(w Way) error
	AddressWay      func(w Way) error
	AddressRelation func(r Relation) error
}
