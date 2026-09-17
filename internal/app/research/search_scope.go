package research

import "fmt"

type PublicationYears struct {
	From int `json:"from"`
	To   int `json:"to"`
}

func (y PublicationYears) Validate() error {
	if y.From != 0 && (y.From < 1000 || y.From > 3000) || y.To != 0 && (y.To < 1000 || y.To > 3000) || y.From != 0 && y.To != 0 && y.From > y.To {
		return fmt.Errorf("invalid publication year range")
	}
	return nil
}
func (y PublicationYears) Contains(year int) bool {
	return year == 0 || (y.From == 0 || year >= y.From) && (y.To == 0 || year <= y.To)
}
func (y PublicationYears) Active() bool { return y.From != 0 || y.To != 0 }
