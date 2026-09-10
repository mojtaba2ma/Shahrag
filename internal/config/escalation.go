package config

// Progressive (multi-stage) banning.
//
// A single fixed ban length is the wrong shape for the problem. It has to
// be simultaneously short enough that a mistake does not lock a real
// visitor out for a day, and long enough that a scanner does not simply
// wait it out and come back. Those two requirements contradict each other,
// and any single number picked is wrong for one of them.
//
// The way out is to stop treating every offender the same. A first offence
// is ambiguous — a shared mobile address behind carrier NAT, a badly
// configured monitoring script, a browser retrying a dead link. A SECOND
// offence from the same address after it has already served a ban is not
// ambiguous at all: it is deliberate, and it deserves a much longer answer.
//
// So the ban length climbs each time the same address comes back:
//
//	first time    30 minutes   — cheap to be wrong about
//	second         2 hours
//	third          8 hours
//	fourth        24 hours
//	fifth          7 days
//	sixth         30 days
//	seventh       permanent
//
// Two properties of that ladder are deliberate:
//
//   - The FIRST step is short. In Iran most residential and every mobile
//     connection is behind carrier-grade NAT, so one address can be
//     thousands of real people. A four-hour first ban on a CGNAT address
//     takes a neighbourhood off the site because one phone on it ran a
//     buggy app. Thirty minutes bounds that damage while still being long
//     enough to make a wordlist scan pointless — a scanner doing 20
//     requests a second loses 36,000 requests to it.
//
//   - The ladder becomes harsh FAST after that, because the evidence gets
//     much stronger with each repeat. Reaching step seven takes seven
//     separate deliberate returns spread over more than a month. Nothing
//     accidental gets there.
//
// Forgiveness is the other half, and it matters as much as the escalation:
// an address that stops misbehaving has to be able to come back down, or
// the ladder is just a slow permanent ban for anyone who ever shares an
// address with an attacker. A record loses ONE step for every clean decay
// period (three days by default), so a genuinely clean address walks all
// the way back to zero, and one that comes back on the fourth day does not
// get a free reset.
//
// Everything here is editable: the number of steps, each step's length,
// and the decay period.

import (
	"fmt"
	"time"
)

// PermanentStep is the step value meaning "never expires". Minutes are
// otherwise positive, so a negative value cannot collide with a real
// duration.
const PermanentStep = -1

// BanEscalation configures the ladder.
type BanEscalation struct {
	// Enabled turns progressive banning on. When off, every ban uses its
	// rule's own fixed length, exactly as before.
	Enabled bool `json:"enabled"`

	// Steps is the ladder, in minutes, shortest first. Step N is used for
	// an address's Nth ban. PermanentStep (-1) means "forever" and is only
	// allowed as the last step, because nothing after a permanent ban can
	// ever be reached.
	//
	// An address that has already served every step stays on the last one
	// for ever after — so a ladder that does not end in PermanentStep is
	// perfectly valid and simply tops out.
	Steps []int `json:"steps,omitempty"`

	// DecayHours is how long an address must go without offending to lose
	// one step. Zero means the shipped default.
	//
	// Decay by one step rather than a full reset: a full reset hands a
	// patient attacker a clean slate for the price of waiting once, while
	// step decay makes them wait once PER STEP they climbed.
	DecayHours int `json:"decay_hours,omitempty"`
}

// Shipped ladder and decay. See the package comment for the reasoning
// behind each number.
var defaultEscalationSteps = []int{
	30,    // 30 minutes
	120,   // 2 hours
	480,   // 8 hours
	1440,  // 24 hours
	10080, // 7 days
	43200, // 30 days
	PermanentStep,
}

const (
	// DefaultDecayHours is the clean period that buys back one step.
	DefaultDecayHours = 72 // 3 days
	// MaxEscalationSteps bounds the ladder. Twelve steps starting at
	// thirty minutes already spans years; more is a configuration
	// mistake, and each step is a row in the panel.
	MaxEscalationSteps = 12
	// maxStepMinutes is one year. Beyond that "permanent" is the honest
	// word for it, and the last step can say so explicitly.
	maxStepMinutes = 525600
	// maxDecayHours is one year of clean behaviour to buy back a step,
	// which is already far past useful.
	maxDecayHours = 8760
)

// DefaultEscalation returns the shipped ladder.
//
// Enabled by default, unlike most new behaviour here. It only ever takes
// effect when a ban is actually being applied, and at that point the
// alternative — the old fixed length — is strictly worse in both
// directions: harsher on a first mistake and softer on a repeat offender.
// Turning it on changes nothing for an install that never bans anybody.
func DefaultEscalation() BanEscalation {
	steps := make([]int, len(defaultEscalationSteps))
	copy(steps, defaultEscalationSteps)
	return BanEscalation{Enabled: true, Steps: steps, DecayHours: DefaultDecayHours}
}

// EffectiveSteps returns the ladder actually in force, falling back to the
// default for a config written before this existed.
func (e BanEscalation) EffectiveSteps() []int {
	if len(e.Steps) == 0 {
		out := make([]int, len(defaultEscalationSteps))
		copy(out, defaultEscalationSteps)
		return out
	}
	return e.Steps
}

// EffectiveDecayHours returns the clean period that buys back one step.
func (e BanEscalation) EffectiveDecayHours() int {
	if e.DecayHours <= 0 {
		return DefaultDecayHours
	}
	return e.DecayHours
}

// DecayPeriod returns EffectiveDecayHours as a duration.
func (e BanEscalation) DecayPeriod() time.Duration {
	return time.Duration(e.EffectiveDecayHours()) * time.Hour
}

// StepCount is how many rungs the ladder has.
func (e BanEscalation) StepCount() int { return len(e.EffectiveSteps()) }

// DurationForLevel returns how long the level-th ban lasts.
//
// level is 1-based: level 1 is an address's first ban. A level past the end
// of the ladder stays on the last step, so an address cannot escape by
// climbing beyond the configured rungs.
func (e BanEscalation) DurationForLevel(level int) (d time.Duration, permanent bool) {
	steps := e.EffectiveSteps()
	if len(steps) == 0 {
		return DefaultBanMinutes * time.Minute, false
	}
	if level < 1 {
		level = 1
	}
	if level > len(steps) {
		level = len(steps)
	}
	m := steps[level-1]
	if m < 0 {
		// Represented as a far-future expiry so no caller needs a
		// special case; the flag carries the intent.
		return 100 * 365 * 24 * time.Hour, true
	}
	if m == 0 {
		return DefaultBanMinutes * time.Minute, false
	}
	return time.Duration(m) * time.Minute, false
}

// ValidateEscalation checks an operator-supplied ladder.
func ValidateEscalation(e BanEscalation) error {
	if !e.Enabled {
		return nil
	}
	steps := e.EffectiveSteps()
	if len(steps) == 0 {
		return fmt.Errorf("progressive banning needs at least one step")
	}
	if len(steps) > MaxEscalationSteps {
		return fmt.Errorf("a ladder of more than %d steps is not useful", MaxEscalationSteps)
	}
	prev := 0
	for i, m := range steps {
		if m == PermanentStep {
			if i != len(steps)-1 {
				// Anything after a permanent ban is unreachable, and a
				// setting that silently does nothing is worse than one
				// that is refused.
				return fmt.Errorf("a permanent step can only be the last one, because no step after it could ever be reached")
			}
			continue
		}
		if m < 1 {
			return fmt.Errorf("step %d must be at least one minute, or permanent", i+1)
		}
		if m > maxStepMinutes {
			return fmt.Errorf("step %d is longer than a year; use a permanent step instead", i+1)
		}
		if m < prev {
			// A ladder that goes down rewards a repeat offender with a
			// shorter ban than their first.
			return fmt.Errorf("step %d is shorter than the step before it, so repeating the offence would be rewarded", i+1)
		}
		prev = m
	}
	if e.DecayHours < 0 {
		return fmt.Errorf("the forgiveness period cannot be negative")
	}
	if e.DecayHours > maxDecayHours {
		return fmt.Errorf("a forgiveness period longer than a year means an address effectively never recovers")
	}
	return nil
}

// EscalationWarnings returns non-fatal remarks about a ladder: things worth
// saying out loud that are not wrong enough to refuse.
func EscalationWarnings(e BanEscalation) []string {
	if !e.Enabled {
		return nil
	}
	var out []string
	steps := e.EffectiveSteps()
	if len(steps) > 0 && steps[0] >= 1440 {
		out = append(out, "The first step is a day or longer. Most residential and all mobile connections in Iran are behind carrier-grade NAT, so one address can be thousands of real people; a long first ban makes a single mistake very expensive.")
	}
	if len(steps) == 1 {
		out = append(out, "There is only one step, so this behaves exactly like a fixed ban length.")
	}
	if len(steps) > 0 && steps[len(steps)-1] == PermanentStep && len(steps) <= 2 {
		out = append(out, "An address reaches a permanent ban after only a couple of offences. That is very fast for a decision that is never undone automatically.")
	}
	return out
}
