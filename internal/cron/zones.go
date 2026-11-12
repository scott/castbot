package cron

import (
	_ "embed"
	"strings"
	"time"
	_ "time/tzdata"
)

//go:embed zones.txt
var zoneList string

// Zone is one IANA zone and the label shown in the picker.
type Zone struct {
	Name  string
	Label string
}

// Zones returns every embedded IANA zone. The user's zone is first.
func Zones(userZone string) []Zone {
	userZone = strings.TrimSpace(userZone)
	var rest []Zone
	var mine *Zone
	for _, name := range strings.Split(zoneList, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		z := Zone{Name: name, Label: zoneLabel(name)}
		if name == userZone {
			copy := z
			mine = &copy
			continue
		}
		rest = append(rest, z)
	}
	if mine == nil && userZone != "" {
		if _, err := time.LoadLocation(userZone); err == nil {
			mine = &Zone{Name: userZone, Label: zoneLabel(userZone)}
		}
	}
	if mine == nil {
		return rest
	}
	return append([]Zone{*mine}, rest...)
}

// ZoneLabel is the picker text for one zone, such as "Mountain Time (America/Denver)".
func ZoneLabel(name string) string { return zoneLabel(name) }

// Friendly is the short name without the IANA suffix, such as "Mountain Time".
func Friendly(name string) string {
	label := zoneLabel(name)
	if i := strings.LastIndex(label, " ("); i > 0 {
		return label[:i]
	}
	return label
}

func zoneLabel(name string) string {
	if nice, ok := niceNames[name]; ok {
		return nice + " (" + name + ")"
	}
	city := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		city = name[i+1:]
	}
	city = strings.ReplaceAll(city, "_", " ")
	return city + " (" + name + ")"
}

var niceNames = map[string]string{
	"America/New_York":             "Eastern Time",
	"America/Detroit":              "Eastern Time",
	"America/Indiana/Indianapolis": "Eastern Time",
	"America/Chicago":              "Central Time",
	"America/Denver":               "Mountain Time",
	"America/Boise":                "Mountain Time",
	"America/Phoenix":              "Arizona Time",
	"America/Los_Angeles":          "Pacific Time",
	"America/Anchorage":            "Alaska Time",
	"America/Juneau":               "Alaska Time",
	"Pacific/Honolulu":             "Hawaii Time",
	"America/Toronto":              "Eastern Time",
	"America/Vancouver":            "Pacific Time",
	"America/Edmonton":             "Mountain Time",
	"America/Winnipeg":             "Central Time",
	"UTC":                          "UTC",
	"Europe/London":                "United Kingdom Time",
	"Europe/Paris":                 "Central European Time",
	"Europe/Berlin":                "Central European Time",
	"Asia/Tokyo":                   "Japan Time",
	"Asia/Shanghai":                "China Time",
	"Asia/Kolkata":                 "India Time",
	"Asia/Singapore":               "Singapore Time",
	"Australia/Sydney":             "Australian Eastern Time",
	"Pacific/Auckland":             "New Zealand Time",
}
