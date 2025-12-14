package osmmini

type Tags map[string]string

type Node struct {
	ID   int64
	Lat  float64
	Lon  float64
	Tags Tags
}

type Way struct {
	ID      int64
	NodeIDs []int64 // optional (Options.EmitWayNodeIDs)
	Tags    Tags
}

type MemberType uint8

const (
	MemberNode MemberType = iota
	MemberWay
	MemberRelation
)

type Member struct {
	Type MemberType
	ID   int64
	Role string
}

type Relation struct {
	ID      int64
	Members []Member // optional (Options.EmitRelationMembers)
	Tags    Tags
}

type Options struct {
	// Wenn nil: alle Tags behalten. Wird nur auf die OUTPUT-Tags angewandt (nicht auf die Filterlogik).
	KeepTag func(key string) bool

	// Wenn false: emitted Ways haben NodeIDs=nil (spart RAM/CPU).
	EmitWayNodeIDs bool

	// Wenn false: emitted Relations haben Members=nil.
	EmitRelationMembers bool
}

type Callbacks struct {
	// Straßen: Ways mit key "highway"
	HighwayWay func(w Way) error

	// Adressen: Objekte mit mindestens einem key "addr:*"
	AddressNode     func(n Node) error
	AddressWay      func(w Way) error
	AddressRelation func(r Relation) error

	// Node wird für alle Knoten aufgerufen (optional, unabhängig von Adresstags).
	Node func(id int64, lat, lon float64) error
}
