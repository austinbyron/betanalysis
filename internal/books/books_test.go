package books

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/austinbyron/betanalysis/pkg/types"
)

// fixtureFetcher serves testdata files by URL substring
type fixtureFetcher struct {
	files map[string]string // url substring -> path
	urls  []string
}

func (f *fixtureFetcher) Get(url string) ([]byte, error) {
	f.urls = append(f.urls, url)
	for sub, path := range f.files {
		if contains(url, sub) {
			return os.ReadFile(path)
		}
	}
	return nil, errors.New("no fixture for " + url)
}

func contains(s, sub string) bool { return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func f64(v float64) *float64 { return &v }

func TestBovadaParsesGameLines(t *testing.T) {
	src := NewBovada(&fixtureFetcher{files: map[string]string{"football/nfl": "testdata/bovada_nfl.json"}})
	if src.Name() != "bovada" {
		t.Errorf("name = %q", src.Name())
	}
	lines, err := src.Fetch("americanfootball_nfl")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	l := lines[0]
	if l.Home != "Seattle Seahawks" || l.Away != "New England Patriots" {
		t.Errorf("teams = %q @ %q", l.Away, l.Home)
	}
	if l.Bookmaker != "bovada" {
		t.Errorf("bookmaker = %q", l.Bookmaker)
	}
	if want := time.UnixMilli(1788999600000).UTC(); !l.Commence.Equal(want) {
		t.Errorf("commence = %v, want %v", l.Commence, want)
	}
	if l.HomeML == nil || l.AwayML == nil || *l.AwayML != 2.6 || *l.HomeML < 1.54 || *l.HomeML > 1.55 {
		t.Errorf("moneyline = %v/%v, want home ~1.5405 away 2.6", deref(l.HomeML), deref(l.AwayML))
	}
	if l.HomeSpread == nil || *l.HomeSpread != -3.5 || l.AwaySpread == nil || *l.AwaySpread != 3.5 {
		t.Errorf("spread = %v/%v, want -3.5/+3.5", deref(l.HomeSpread), deref(l.AwaySpread))
	}
	if l.HomeSpreadOdds == nil || l.AwaySpreadOdds == nil {
		t.Error("spread odds missing")
	}
	if l.Total == nil || *l.Total != 44 || l.OverOdds == nil || l.UnderOdds == nil {
		t.Errorf("total = %v (%v/%v), want 44", deref(l.Total), deref(l.OverOdds), deref(l.UnderOdds))
	}
}

type bytesFetcher []byte

func (b bytesFetcher) Get(string) ([]byte, error) { return []byte(b), nil }

func TestBovadaEmptySlateIsNotAnError(t *testing.T) {
	lines, err := NewBovada(bytesFetcher("{}")).Fetch("baseball_mlb")
	if err != nil || len(lines) != 0 {
		t.Errorf("empty slate: lines=%v err=%v", lines, err)
	}
}

func TestBovadaUnsupportedSport(t *testing.T) {
	src := NewBovada(&fixtureFetcher{})
	if _, err := src.Fetch("soccer_epl"); !errors.Is(err, ErrUnsupportedSport) {
		t.Errorf("err = %v, want ErrUnsupportedSport", err)
	}
}

func TestDraftKingsParsesMainMarkets(t *testing.T) {
	src := NewDraftKings(&fixtureFetcher{files: map[string]string{
		"leagues/88808": "testdata/draftkings_nfl.json",
		"leagues/84240": "testdata/draftkings_mlb.json",
		"leagues/87637": "testdata/draftkings_ncaaf.json",
	}})
	if src.Name() != "draftkings" {
		t.Errorf("name = %q", src.Name())
	}

	nfl, err := src.Fetch("americanfootball_nfl")
	if err != nil {
		t.Fatalf("Fetch nfl: %v", err)
	}
	if len(nfl) != 2 {
		t.Fatalf("nfl lines = %d, want 2", len(nfl))
	}
	l := nfl[0]
	if l.Home != "SEA Seahawks" || l.Away != "NE Patriots" || l.Bookmaker != "draftkings" {
		t.Errorf("line = %+v", l)
	}
	if want := time.Date(2026, 9, 10, 0, 20, 0, 0, time.UTC); !l.Commence.Equal(want) {
		t.Errorf("commence = %v, want %v", l.Commence, want)
	}
	if l.AwayML == nil || *l.AwayML != 2.5 || l.HomeML == nil || *l.HomeML < 1.55 || *l.HomeML > 1.56 {
		t.Errorf("moneyline = %v/%v", deref(l.HomeML), deref(l.AwayML))
	}
	if l.HomeSpread == nil || l.AwaySpread == nil || *l.HomeSpread != -*l.AwaySpread {
		t.Errorf("spread = %v/%v", deref(l.HomeSpread), deref(l.AwaySpread))
	}
	if l.Total == nil || l.OverOdds == nil || l.UnderOdds == nil {
		t.Errorf("total missing: %+v", l)
	}

	// MLB fixture: first event was already in progress (skipped); DK names
	// the run line differently so MLB carries moneyline + total only
	mlb, err := src.Fetch("baseball_mlb")
	if err != nil || len(mlb) != 1 || mlb[0].Home != "PHI Phillies" || mlb[0].HomeML == nil || mlb[0].Total == nil {
		t.Errorf("mlb = %v, %+v", err, mlb)
	}
	ncaaf, err := src.Fetch("americanfootball_ncaaf")
	if err != nil || len(ncaaf) != 2 || ncaaf[0].Away != "SMU" {
		t.Errorf("ncaaf = %v, %+v", err, ncaaf)
	}
}

func TestDraftKingsSkipsStartedEvents(t *testing.T) {
	fx := &fixtureFetcher{files: map[string]string{"leagues/88808": "testdata/draftkings_nfl_started.json"}}
	src := NewDraftKings(fx)
	lines, err := src.Fetch("americanfootball_nfl")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Errorf("lines = %d, want 1 (STARTED event skipped)", len(lines))
	}
}

func TestMatchLinksLinesToGamesByNicknameAndTime(t *testing.T) {
	kick := time.Date(2026, 9, 10, 0, 20, 0, 0, time.UTC)
	games := []types.Game{
		{ID: "g1", SportKey: "americanfootball_nfl", HomeTeam: "Seattle Seahawks", AwayTeam: "New England Patriots", CommenceTime: kick},
		{ID: "g2", SportKey: "americanfootball_nfl", HomeTeam: "Los Angeles Rams", AwayTeam: "San Francisco 49ers", CommenceTime: kick.Add(72 * time.Hour)},
		{ID: "g3", SportKey: "americanfootball_ncaaf", HomeTeam: "Florida State Seminoles", AwayTeam: "SMU Mustangs", CommenceTime: kick},
	}
	now := time.Date(2026, 9, 7, 5, 0, 0, 0, time.UTC)
	lines := []Line{
		{Home: "SEA Seahawks", Away: "NE Patriots", Commence: kick, Bookmaker: "draftkings",
			HomeML: f64(1.55), AwayML: f64(2.5), HomeSpread: f64(-3.5), AwaySpread: f64(3.5),
			HomeSpreadOdds: f64(1.91), AwaySpreadOdds: f64(1.91), Total: f64(44), OverOdds: f64(1.87), UnderOdds: f64(1.95)},
		{Home: "Florida State", Away: "SMU", Commence: kick.Add(30 * time.Minute), Bookmaker: "draftkings", HomeML: f64(1.4), AwayML: f64(3.0)},
		{Home: "Dallas Cowboys", Away: "Philadelphia Eagles", Commence: kick, Bookmaker: "draftkings", HomeML: f64(2), AwayML: f64(1.9)},                     // not in slate
		{Home: "Seattle Seahawks", Away: "New England Patriots", Commence: kick.Add(30 * time.Hour), Bookmaker: "bovada", HomeML: f64(1.5), AwayML: f64(2.6)}, // too far even for the same-day fallback
	}

	odds, unmatched := Match(lines, games, "draftkings-scrape", now)
	if unmatched != 2 {
		t.Errorf("unmatched = %d, want 2", unmatched)
	}
	byKey := map[string]types.GameOdds{}
	for _, o := range odds {
		byKey[o.GameID+"/"+o.MarketType] = o
	}
	ml, ok := byKey["g1/"+types.MarketMoneyline]
	if !ok || *ml.HomeOdds != 1.55 || *ml.AwayOdds != 2.5 || ml.Bookmaker != "draftkings" || ml.Source != "draftkings-scrape" {
		t.Errorf("g1 moneyline = %+v", ml)
	}
	if !ml.RetrievedAt.Equal(now) || !ml.LastUpdate.Equal(now) {
		t.Errorf("timestamps should be the poll time: %+v", ml)
	}
	sp := byKey["g1/"+types.MarketSpread]
	if sp.HomeSpread == nil || *sp.HomeSpread != -3.5 || *sp.AwaySpread != 3.5 || *sp.HomeOdds != 1.91 {
		t.Errorf("g1 spread = %+v", sp)
	}
	tot := byKey["g1/"+types.MarketTotals]
	if tot.OverUnder == nil || *tot.OverUnder != 44 || *tot.OverOdds != 1.87 || *tot.UnderOdds != 1.95 {
		t.Errorf("g1 total = %+v", tot)
	}
	if _, ok := byKey["g3/"+types.MarketMoneyline]; !ok {
		t.Error("NCAAF line should match by name subset (SMU -> SMU Mustangs)")
	}
	if _, ok := byKey["g3/"+types.MarketSpread]; ok {
		t.Error("no spread row when the line has no spread")
	}
	if len(odds) != 4 {
		t.Errorf("odds rows = %d, want 4 (3 for g1 + 1 for g3)", len(odds))
	}
}

func TestMatchPrefersMoreSpecificNameOnSharedKickoff(t *testing.T) {
	kick := time.Date(2026, 9, 12, 16, 0, 0, 0, time.UTC)
	games := []types.Game{
		{ID: "gsu", SportKey: "americanfootball_ncaaf", HomeTeam: "Kennesaw State Owls", AwayTeam: "Georgia State Panthers", CommenceTime: kick},
		{ID: "uga", SportKey: "americanfootball_ncaaf", HomeTeam: "Georgia Bulldogs", AwayTeam: "Western Kentucky Hilltoppers", CommenceTime: kick},
	}
	lines := []Line{{Home: "Georgia", Away: "Western Kentucky", Commence: kick, Bookmaker: "draftkings", HomeML: f64(1.05), AwayML: f64(12)}}
	odds, unmatched := Match(lines, games, "x", kick)
	if unmatched != 0 || len(odds) != 1 || odds[0].GameID != "uga" {
		t.Errorf("odds = %+v unmatched %d, want uga", odds, unmatched)
	}
}

func TestMatchFallsBackToSameDayForPlaceholderKickoffs(t *testing.T) {
	placeholder := time.Date(2026, 9, 12, 16, 0, 0, 0, time.UTC) // aggregator TBD slot
	real := time.Date(2026, 9, 13, 2, 15, 0, 0, time.UTC)        // book's evening kickoff
	games := []types.Game{{ID: "utah", SportKey: "americanfootball_ncaaf", HomeTeam: "Utah Utes", AwayTeam: "Arkansas Razorbacks", CommenceTime: placeholder}}
	lines := []Line{{Home: "Utah (#21)", Away: "Arkansas", Commence: real, Bookmaker: "bovada", HomeML: f64(1.6), AwayML: f64(2.4)}}
	odds, unmatched := Match(lines, games, "x", real)
	if unmatched != 0 || len(odds) != 1 || odds[0].GameID != "utah" {
		t.Errorf("odds = %+v unmatched %d, want utah via same-day fallback", odds, unmatched)
	}

	// The fallback refuses when the pair is not unique in the day (doubleheader)
	dh := []types.Game{
		{ID: "dh1", SportKey: "baseball_mlb", HomeTeam: "Chicago Cubs", AwayTeam: "St. Louis Cardinals", CommenceTime: placeholder},
		{ID: "dh2", SportKey: "baseball_mlb", HomeTeam: "Chicago Cubs", AwayTeam: "St. Louis Cardinals", CommenceTime: placeholder.Add(4 * time.Hour)},
	}
	far := []Line{{Home: "CHI Cubs", Away: "STL Cardinals", Commence: placeholder.Add(10 * time.Hour), Bookmaker: "draftkings", HomeML: f64(1.8), AwayML: f64(2.0)}}
	if odds, unmatched := Match(far, dh, "x", real); unmatched != 1 || len(odds) != 0 {
		t.Errorf("ambiguous same-day pair must stay unmatched: odds=%+v unmatched=%d", odds, unmatched)
	}
}

func TestMatchPrefersClosestStartForDoubleheaders(t *testing.T) {
	first := time.Date(2026, 9, 12, 17, 0, 0, 0, time.UTC)
	games := []types.Game{
		{ID: "dh1", SportKey: "baseball_mlb", HomeTeam: "Chicago Cubs", AwayTeam: "St. Louis Cardinals", CommenceTime: first},
		{ID: "dh2", SportKey: "baseball_mlb", HomeTeam: "Chicago Cubs", AwayTeam: "St. Louis Cardinals", CommenceTime: first.Add(4 * time.Hour)},
	}
	lines := []Line{
		{Home: "CHI Cubs", Away: "STL Cardinals", Commence: first.Add(4*time.Hour + 5*time.Minute), Bookmaker: "draftkings", HomeML: f64(1.8), AwayML: f64(2.0)},
	}
	odds, unmatched := Match(lines, games, "x", first)
	if unmatched != 0 || len(odds) != 1 || odds[0].GameID != "dh2" {
		t.Errorf("odds = %+v unmatched %d, want dh2", odds, unmatched)
	}
}

func TestTeamsCompatible(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"NE Patriots", "New England Patriots", true},
		{"LA Dodgers", "Los Angeles Dodgers", true},
		{"LA Angels", "Los Angeles Dodgers", false},
		{"SMU", "SMU Mustangs", true},
		{"SMU (#19)", "SMU Mustangs", true},
		{"Florida State", "Florida State Seminoles", true},
		{"Florida", "Florida State Seminoles", false},
		{"NY Mets", "New York Yankees", false},
		{"Athletics", "Athletics", true},
		{"St. Louis Cardinals", "STL Cardinals", true},
		// Real week-2 college spellings from DraftKings / Bovada vs The Odds API
		{"Alabama", "Alabama Crimson Tide", true},
		{"Alabama (#13)", "Alabama Crimson Tide", true},
		{"Duke", "Duke Blue Devils", true},
		{"Rutgers", "Rutgers Scarlet Knights", true},
		{"Illinois", "Illinois Fighting Illini", true},
		{"Army", "Army Black Knights", true},
		{"Texas", "Texas Tech Red Raiders", false},
		{"Texas", "Texas A&M Aggies", false},
		{"Texas", "Texas Longhorns", true},
		{"Ohio", "Ohio State Buckeyes", false},
		{"Ohio", "Ohio Bobcats", true},
		{"Miami", "Miami (OH) RedHawks", false},
		{"Miami", "Miami Hurricanes", true},
		{"Miami (OH)", "Miami (OH) RedHawks", true},
		{"Miami (OH)", "Miami Hurricanes", false},
		{"Central Florida", "UCF Knights", true},
		{"Connecticut", "UConn Huskies", true},
		{"UL Lafayette", "Louisiana Ragin Cajuns", true},
		{"Louisiana", "Louisiana Ragin Cajuns", true},
		{"Louisiana", "Louisiana Tech Bulldogs", false},
		{"ULM", "UL Monroe Warhawks", true},
		{"Southern Miss", "Southern Mississippi Golden Eagles", true},
		{"FIU", "Florida International Panthers", true},
		{"Middle Tennessee State", "Middle Tennessee Blue Raiders", true},
		{"Middle Tennessee", "Middle Tennessee Blue Raiders", true},
		{"Sam Houston", "Sam Houston State Bearkats", true},
		{"Sam Houston State", "Sam Houston State Bearkats", true},
		{"Georgia Bulldogs", "Mississippi State Bulldogs", false},
		{"Georgia", "Georgia Tech Yellow Jackets", false},
		{"Georgia", "Georgia Southern Eagles", false},
		{"Georgia", "Georgia Bulldogs", true},
	}
	for _, c := range cases {
		if got := teamsCompatible(c.a, c.b); got != c.want {
			t.Errorf("teamsCompatible(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
