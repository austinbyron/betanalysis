package books

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Bovada reads the public JSON behind bovada.lv's sport pages. No auth,
// no bot wall as of 2026-09; the marketFilterId=def query some guides
// mention returns {} — omit it.
type Bovada struct {
	fetch Fetcher
}

// NewBovada creates the source over a fetcher
func NewBovada(f Fetcher) *Bovada { return &Bovada{fetch: f} }

// Name implements Source
func (b *Bovada) Name() string { return "bovada" }

var bovadaPaths = map[string]string{
	"baseball_mlb":           "baseball/mlb",
	"americanfootball_nfl":   "football/nfl",
	"americanfootball_ncaaf": "football/college-football",
	"basketball_nba":         "basketball/nba",
	"basketball_ncaab":       "basketball/college-basketball",
}

type bovadaGroup struct {
	Events []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
		StartTime   int64  `json:"startTime"`
		Live        bool   `json:"live"`
		Type        string `json:"type"`
		Competitors []struct {
			Name string `json:"name"`
			Home bool   `json:"home"`
		} `json:"competitors"`
		DisplayGroups []struct {
			Description string `json:"description"`
			Markets     []struct {
				Description string `json:"description"`
				Period      struct {
					Main bool `json:"main"`
				} `json:"period"`
				Outcomes []struct {
					Description string `json:"description"`
					Type        string `json:"type"` // H, A, O, U
					Price       struct {
						Decimal  string `json:"decimal"`
						Handicap string `json:"handicap"`
					} `json:"price"`
				} `json:"outcomes"`
			} `json:"markets"`
		} `json:"displayGroups"`
	} `json:"events"`
}

// Fetch implements Source
func (b *Bovada) Fetch(sportKey string) ([]Line, error) {
	path, ok := bovadaPaths[sportKey]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSport, sportKey)
	}
	body, err := b.fetch.Get("https://www.bovada.lv/services/sports/event/v2/events/A/description/" + path)
	if err != nil {
		return nil, err
	}
	// An empty slate comes back as {} rather than []
	if trimmed := bytes.TrimSpace(body); len(trimmed) == 0 || trimmed[0] == '{' {
		return nil, nil
	}
	var groups []bovadaGroup
	if err := json.Unmarshal(body, &groups); err != nil {
		return nil, fmt.Errorf("bovada: decode: %w", err)
	}

	var lines []Line
	for _, g := range groups {
		for _, ev := range g.Events {
			if ev.Live || ev.Type != "GAMEEVENT" || len(ev.Competitors) != 2 {
				continue
			}
			l := Line{Bookmaker: "bovada", Commence: time.UnixMilli(ev.StartTime).UTC()}
			for _, c := range ev.Competitors {
				if c.Home {
					l.Home = c.Name
				} else {
					l.Away = c.Name
				}
			}
			if l.Home == "" || l.Away == "" {
				continue
			}
			for _, dg := range ev.DisplayGroups {
				if dg.Description != "Game Lines" {
					continue
				}
				for _, m := range dg.Markets {
					if !m.Period.Main {
						continue
					}
					for _, o := range m.Outcomes {
						price := parseFloat(o.Price.Decimal)
						hcap := parseFloat(o.Price.Handicap)
						switch m.Description {
						case "Moneyline":
							switch o.Type {
							case "H":
								l.HomeML = price
							case "A":
								l.AwayML = price
							}
						case "Point Spread":
							switch o.Type {
							case "H":
								l.HomeSpread, l.HomeSpreadOdds = hcap, price
							case "A":
								l.AwaySpread, l.AwaySpreadOdds = hcap, price
							}
						case "Total":
							switch o.Type {
							case "O":
								l.Total, l.OverOdds = hcap, price
							case "U":
								if l.Total == nil {
									l.Total = hcap
								}
								l.UnderOdds = price
							}
						}
					}
				}
			}
			if l.HomeML != nil || l.AwayML != nil || l.Total != nil || l.HomeSpread != nil {
				lines = append(lines, l)
			}
		}
	}
	return lines, nil
}

func parseFloat(s string) *float64 {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}
