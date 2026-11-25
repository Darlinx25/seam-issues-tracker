package querypicker

type Column struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Width string `json:"width,omitempty"`
}

type Spec struct {
	Query       string
	ValueField  string
	LabelFields []string
	Columns     []Column
	Limit       int
	Dropdown    string
}
