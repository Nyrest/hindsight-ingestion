package googlepeople

import "time"

type person struct {
	ResourceName string `json:"resourceName"`
	Metadata     struct {
		Deleted               bool           `json:"deleted"`
		Sources               []personSource `json:"sources"`
		PreviousResourceNames []string       `json:"previousResourceNames"`
	} `json:"metadata"`
	Names          []personField `json:"names"`
	Nicknames      []personField `json:"nicknames"`
	EmailAddresses []personField `json:"emailAddresses"`
	PhoneNumbers   []personField `json:"phoneNumbers"`
	Organizations  []personField `json:"organizations"`
	Addresses      []personField `json:"addresses"`
	Birthdays      []personField `json:"birthdays"`
	Events         []personField `json:"events"`
	Relations      []personField `json:"relations"`
	Biographies    []personField `json:"biographies"`
	URLs           []personField `json:"urls"`
	Memberships    []struct {
		ContactGroupMembership struct {
			ResourceName string `json:"contactGroupResourceName"`
		} `json:"contactGroupMembership"`
	} `json:"memberships"`
}

type personSource struct {
	Type       string    `json:"type"`
	ID         string    `json:"id"`
	UpdateTime time.Time `json:"updateTime"`
}

// People fields share labels and primary metadata, with type-specific values.
type personField struct {
	Metadata struct {
		Primary bool `json:"primary"`
	} `json:"metadata"`
	Value          string `json:"value"`
	Type           string `json:"type"`
	DisplayName    string `json:"displayName"`
	GivenName      string `json:"givenName"`
	FamilyName     string `json:"familyName"`
	Name           string `json:"name"`
	Title          string `json:"title"`
	Department     string `json:"department"`
	JobDescription string `json:"jobDescription"`
	FormattedValue string `json:"formattedValue"`
	StreetAddress  string `json:"streetAddress"`
	City           string `json:"city"`
	Region         string `json:"region"`
	PostalCode     string `json:"postalCode"`
	Country        string `json:"country"`
	Person         string `json:"person"`
	ContentType    string `json:"contentType"`
	Text           string `json:"text"`
	Date           struct {
		Year  int `json:"year"`
		Month int `json:"month"`
		Day   int `json:"day"`
	} `json:"date"`
}

type contactSnapshot struct {
	ID            string
	Name          string
	Text          string
	Names         []string
	Emails        []string
	Phones        []string
	Organizations []string
	ModifiedAt    time.Time
	Deleted       bool
}
