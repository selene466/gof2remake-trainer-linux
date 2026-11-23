package trainer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

type CatItem struct {
	ID   int
	Name string
	Cat  int
}

var CatNames = []string{"Primary weapons", "Secondary weapons", "Turrets", "Equipment", "Commodities"}

// LoadCatalog reads the item names (English text table) and categories (item_attributes)
// from the game's own resources.assets.
func LoadCatalog(dataDir string) ([]CatItem, error) {
	b, err := os.ReadFile(dataDir + "/resources.assets")
	if err != nil {
		return nil, err
	}
	// item attributes: [{"index":N,"keys":[...],"values":[...]}], key 1 = category
	ai := bytes.Index(b, []byte("item_attributes"))
	if ai < 0 {
		return nil, errors.New("item_attributes not found")
	}
	si := bytes.Index(b[ai:], []byte(`[{"index":0,`))
	if si < 0 {
		return nil, errors.New("item table not found")
	}
	var attrs []struct {
		Index  int   `json:"index"`
		Keys   []int `json:"keys"`
		Values []int `json:"values"`
	}
	if err := json.NewDecoder(bytes.NewReader(b[ai+si:])).Decode(&attrs); err != nil {
		return nil, err
	}
	// names: the English table lists item 0 ("Nirai Impulse EX 1") first, then one per id
	ni := bytes.Index(b, []byte("\"Nirai Impulse EX 1\",\r\n"))
	if ni < 0 {
		return nil, errors.New("name table not found")
	}
	lines := strings.Split(string(b[ni:ni+len(attrs)*80]), "\n")
	var out []CatItem
	for _, a := range attrs {
		if a.Index >= len(lines) {
			break
		}
		ln := strings.TrimRight(strings.TrimSpace(lines[a.Index]), ",")
		var name string
		if json.Unmarshal([]byte(ln), &name) != nil {
			continue
		}
		cat := -1
		for i, k := range a.Keys {
			if k == 1 && i < len(a.Values) {
				cat = a.Values[i]
			}
		}
		if cat < 0 || cat >= len(CatNames) {
			continue
		}
		out = append(out, CatItem{ID: a.Index, Name: name, Cat: cat})
	}
	if len(out) < 100 {
		return nil, errors.New("catalog looks wrong")
	}
	return out, nil
}
