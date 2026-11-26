package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Step is one thing the script makes happen: "death:Alex@5" is
// {Verb: "death", Name: "Alex", At: 5s}.
//
// The verbs every personality should understand are:
//
//	join   the player joins
//	leave  the player leaves
//	death  the player dies
//	deny   the player is turned away (wrong password, not on the allow list)
//	save   the server saves the world
//	count  the server says how many players are on
//
// A personality may add its own; a verb it doesn't handle is reported on
// standard error and otherwise ignored.
type Step struct {
	Verb string
	Name string
	At   time.Duration // after the server said it was ready
}

// parseScript reads "join:Alex@1,death:Alex@5.5,save@7". A step without
// "@" happens at once; one without ":" has no name. The steps come back in
// time order (steps at the same time keep the script's order).
func parseScript(script string) ([]Step, error) {
	var steps []Step
	for part := range strings.SplitSeq(script, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		verbName, at, hasAt := strings.Cut(part, "@")
		verb, name, _ := strings.Cut(verbName, ":")
		st := Step{Verb: strings.ToLower(strings.TrimSpace(verb)), Name: strings.TrimSpace(name)}
		if st.Verb == "" {
			return nil, fmt.Errorf("script step %q has no verb", part)
		}
		if hasAt {
			sec, err := strconv.ParseFloat(strings.TrimSpace(at), 64)
			if err != nil || sec < 0 {
				return nil, fmt.Errorf("script step %q: %q isn't a number of seconds", part, at)
			}
			st.At = time.Duration(sec * float64(time.Second))
		}
		steps = append(steps, st)
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].At < steps[j].At })
	return steps, nil
}

// runScript plays the steps, timed from when it is called (the server
// being ready). Each step runs on the server's goroutine.
func (s *Server) runScript(steps []Step) {
	start := time.Now()
	for _, st := range steps {
		time.Sleep(time.Until(start.Add(st.At)))
		s.post(func() { s.step(st) })
	}
}

// step hands one step to the personality.
func (s *Server) step(st Step) {
	if s.onStep != nil && s.onStep(st) {
		return
	}
	s.Warnf("the %s stand-in has no script step %q", s.p.ID, st.Verb)
}
