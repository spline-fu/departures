package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fatih/color"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gopkg.in/AlecAivazis/survey.v1"
)

func main() {
	// parse the command line arguments
	id := flag.String("id", "", "ID of the stop")
	filterMode := flag.String("filter-mode", "", "Filter the list for this mode of transporation (Comma separated)")
	filterDestination := flag.String("filter-destination", "", "Filter the list for this destination (Comma separated)")
	filterLine := flag.String("filter-line", "", "Filter the list for this line (Comma separated)")
	width := flag.Int("width", intEnv("WTF_WIDGET_WIDTH"), "Width of the output")
	retries := flag.Int("retries", 3, "Number of retries before giving up")
	retryPause := flag.Duration("retry-pause", time.Second, "Pause between retries")
	min := flag.Int("min", 60, "Number of minutes you want to see the departures for")
	forceColor := flag.Bool("force-color", false, "Use this flag to enforce color output even if the terminal does not report support")
	search := flag.String("search", "", "Search for the stop name to get the stop ID")
	stationName := flag.String("station", "", "Fetch departures for given station. Ignored if ID is provided")
	verbose := flag.Bool("verbose", false, "Be verbose and show additional information (mode of transportataion, operator and additional remarks)")
	bicycle := flag.Bool("bicycle", false, "Only show connections that allow bicycle conveyance")
	flag.Parse()

	// ensure valid retry values
	if *retries < 0 {
		*retries = 0
	}
	if *retryPause < 0 {
		*retryPause = 0
	}

	var err error

	// check if the user just wants to find the station ID
	if *search != "" {
		stations, err := searchStations(*search)
		if err != nil {
			fmt.Println("Could not query stations")
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			os.Exit(1)
		}

		fmt.Printf("Found %d station(s):\n", len(stations))
		for _, s := range stations {
			fmt.Printf("  %s - %s\n", s.ID, s.Name)
		}

		return
	}

	// search of the station and provide user option to choose
	if *id == "" && *stationName != "" {
		s, err := promptForStation(*stationName)
		if err != nil {
			fmt.Println(err)
		} else {
			*id = s.ID
		}
	}

	// set default id if empty
	if *id == "" {
		fmt.Println("station ID is empty. Defaulting to: 900051353")
		*id = "900051353"
	}

	// set the color mode
	color.NoColor = color.NoColor && !*forceColor

	// request the departures
	var result result
	for i := 0; i < *retries+3; i++ {
		err = getJSON(&result, "https://v6.vbb.transport.rest/stops/%s/departures?duration=%d", *id, *min)
		if err == nil {
			break
		}
		time.Sleep(*retryPause)
	}
	if err != nil {
		fmt.Println("Could not query departures")
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	// initialize the filters
	fm := filterSlice(*filterMode)
	fd := filterSlice(*filterDestination)
	fl := filterSlice(*filterLine)

	// calculate the length of the columns
	var lenName, lenDir, lenDep, lenRem int
	from := time.Now().Add(-2 * time.Minute)
	until := time.Now().Add(time.Hour)
	filteredDeps := result.Departures[:0] // no need to waste space
	for _, dep := range result.Departures {
		if dep.When.Before(from) || dep.When.After(until) {
			continue
		}

		// trim unnecessary whitespace
		dep.Line.Product = strings.TrimSpace(dep.Line.Product)
		dep.Direction = strings.TrimSpace(dep.Direction)
		dep.Line.Name = strings.TrimSpace(dep.Line.Name)

		// apply filters
		if isFiltered(fm, dep.Line.Product) {
			continue
		}
		if isFiltered(fd, dep.Direction) {
			continue
		}
		if isFiltered(fl, dep.Line.Name) {
			continue
		}
		if *bicycle && filterBike(dep) {
			continue
		}

		// the entry survived the filters, append it to the filtered list
		filteredDeps = append(filteredDeps, dep)

		// update the lengths
		lenName = maxStringLen(dep.Line.Name, lenName)
		lenDir = maxStringLen(dep.Direction, lenDir)
		lenDep = maxStringLen(departureTime(dep), lenDep)
		lenRem = maxStringLen("Operator", lenRem)
		for _, rem := range dep.Remarks {
			lenRem = maxStringLen(rem.Type, lenRem)
		}
	}

	// adjust the column length
	if *width > 0 {
		lenDir = *width - lenName - lenDep - 2
		if lenDir < 1 {
			lenDir = 1
		}
	}

	// render the columns
	for _, dep := range filteredDeps {
		departureColor := color.HiGreenString
		if dep.Delay > 0 {
			departureColor = color.RedString
		} else if dep.Delay < 0 {
			departureColor = color.YellowString
		}

		fmt.Println(
			color.WhiteString("%s", leftPad(dep.Line.Name, lenName)),
			rightPad(dep.Direction, lenDir),
			departureColor("%s", departureTime(dep)),
		)
		if !*verbose {
			continue
		}

		remarkColor := color.WhiteString
		fmt.Println(
			remarkColor("%s", leftPad(rightPad("Operator", lenRem), (lenRem+lenName+1))),
			":",
			dep.Line.Operator.Name,
		)
		fmt.Println(
			remarkColor("%s", leftPad(rightPad("Type", lenRem), (lenRem+lenName+1))),
			":",
			dep.Line.Product,
		)
		for _, rem := range dep.Remarks {
			if rem.Text != "" {
				rem.Type = cases.Title(language.English).String(rem.Type)
				if rem.Type == "Warning" {
					remarkColor = color.RedString
				}
				fmt.Println(
					remarkColor("%s", leftPad(rightPad(rem.Type, lenRem), (lenRem+lenName+1))),
					":",
					rem.Text,
				)
			}
		}
	}
}

func searchStations(name string) ([]station, error) {
	var stations []station
	err := getJSON(&stations, "https://v6.vbb.transport.rest/locations?query=%s&poi=false&addresses=false", name)

	return stations, err
}

func promptForStation(name string) (*station, error) {
	stations, err := searchStations(name)
	if err != nil {
		return nil, fmt.Errorf("could not query stations")
	}

	if l := len(stations); l == 0 {
		// no stations found
		return nil, fmt.Errorf("could not find matching stations")
	} else if l == 1 {
		return &stations[0], nil
	}
	// set first result as fallback
	fallback := stations[0].Name

	// convert to map[string]station to get station after user prompt
	var options []string
	optionStation := map[string]station{}
	for _, s := range stations {
		options = append(options, s.Name)
		optionStation[s.Name] = s
	}

	prompt := &survey.Select{
		Message: "Choose a station:",
		Options: options,
		Default: fallback,
	}

	var choice string
	if err = survey.AskOne(prompt, &choice, nil); err != nil {
		fmt.Println("Failed to get answer on station list. Defaulting to", fallback)
		choice = fallback
	}

	s := optionStation[choice]
	return &s, nil
}

func getJSON(v interface{}, urlFormat string, values ...interface{}) error {
	resp, err := http.Get(fmt.Sprintf(urlFormat, values...))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	d := json.NewDecoder(resp.Body)
	return d.Decode(v)
}

type result struct {
	Departures            []departure `json:"departures"`
	RealtimeDataUpdatedAt int         `json:"realtimeDataUpdatedAt"`
}

type departure struct {
	TripID          string    `json:"tripId"`
	Stop            station   `json:"stop"`
	When            time.Time `json:"when"`
	PlannedWhen     time.Time `json:"plannedWhen"`
	Delay           int       `json:"delay"`
	Platform        string    `json:"platform"`
	PlannedPlatform string    `json:"plannedPlatform"`
	PrognosisType   string    `json:"prognosisType"`
	Direction       string    `json:"direction"`
	Provenance      string    `json:"provenance"`
	Line            struct {
		Type        string `json:"type"`
		ID          string `json:"id"`
		FahrtNr     string `json:"fahrtNr"`
		Name        string `json:"name"`
		Public      bool   `json:"public"`
		AdminCode   string `json:"adminCode"`
		ProductName string `json:"productName"`
		Mode        string `json:"mode"`
		Product     string `json:"product"`
		Operator    struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"operator"`
	} `json:"line"`
	Remarks []struct {
		Type string `json:"type"`
		Code string `json:"code"`
		Text string `json:"text"`
	} `json:"remarks"`
	Origin               station `json:"origin"`
	Destination          station `json:"destination"`
	CurrentTripPoisition struct {
		Type      string  `json:"type"`
		Latidude  float64 `json:"latidude"`
		Longitude float64 `json:"longitude"`
	} `json:"currentTripPoisition"`
}

type station struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location struct {
		Type      string  `json:"type"`
		ID        string  `json:"id"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"location"`
	Products struct {
		Suburban bool `json:"suburban"`
		Subway   bool `json:"subway"`
		Tram     bool `json:"tram"`
		Bus      bool `json:"bus"`
		Ferry    bool `json:"ferry"`
		Express  bool `json:"express"`
		Regional bool `json:"regional"`
	} `json:"products"`
}

func leftPad(s string, l int) string {
	r := []rune(s)
	if len(r) >= l {
		return string(r[:l])
	}

	return strings.Repeat(" ", l-len(r)) + string(r)
}

func rightPad(s string, l int) string {
	r := []rune(s)
	if len(r) >= l {
		return string(r[:l])
	}

	return string(r) + strings.Repeat(" ", l-len(r))
}

func departureTime(r departure) string {
	if r.Delay == 0 {
		return r.When.Format("15:04")
	}
	return fmt.Sprintf("%s (%+d)", r.When.Format("15:04"), r.Delay/60)
}

func filterSlice(filter string) []string {
	if filter == "" {
		return nil
	}

	fs := strings.Split(strings.ToUpper(filter), ",")
	for i, f := range fs {
		fs[i] = strings.TrimSpace(f)
	}
	return fs
}

func isFiltered(filter []string, v string) bool {
	if len(filter) == 0 {
		return false
	}

	for _, f := range filter {
		if strings.EqualFold(f, v) {
			return false
		}
	}
	return true
}

func maxStringLen(s string, l int) int {
	c := utf8.RuneCountInString(s)
	if c > l {
		return c
	}
	return l
}

func intEnv(key string) int {
	i, _ := strconv.Atoi(os.Getenv(key))
	return i
}

func filterBike(r departure) bool {
	for _, rem := range r.Remarks {
		if strings.TrimSpace(rem.Code) == "FB" {
			return false
		}
	}
	return true
}
