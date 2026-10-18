package estimate

// Audience is a reader profile with a baseline silent-reading speed.
type Audience struct {
	ID             string
	Name           string
	Description    string
	WordsPerMinute float64
}

// Audiences returns the built-in reader profiles, from least to most specialised.
func Audiences() []Audience {
	return []Audience{
		{ID: "child", Name: "Child (~10y)", Description: "a 10-year-old child with typical reading skills and no specialist knowledge", WordsPerMinute: 130},
		{ID: "teen", Name: "Teenager", Description: "a high-school student with general schooling and no specialist knowledge", WordsPerMinute: 200},
		{ID: "esl", Name: "Non-native adult", Description: "an adult with intermediate English as a second language and general knowledge", WordsPerMinute: 150},
		{ID: "adult", Name: "General adult", Description: "a college-educated adult with general knowledge but no specialist training in the text's subject", WordsPerMinute: 238},
		{ID: "expert", Name: "Domain expert", Description: "a professional with deep expertise in the text's subject area", WordsPerMinute: 280},
	}
}

type level struct {
	description string
	multiplier  float64
}

// comprehensionLevels scale read time into time to understand what the text says.
func comprehensionLevels() []level {
	return []level{
		{"Understood fully on a single read with no extra effort", 1.0},
		{"Needs occasional pauses or re-reading of a few sentences", 1.4},
		{"Needs slow, careful reading and re-reading of several passages", 2.0},
		{"Needs looking up terms or background information while reading", 3.0},
		{"Needs substantial background study before the text makes sense", 5.0},
	}
}

// grokLevels scale understand time into time to internalise the ideas well enough to explain and apply them.
func grokLevels() []level {
	return []level{
		{"Ideas are simple; once understood they are fully internalised", 1.2},
		{"Needs brief reflection to connect the ideas together", 2.0},
		{"Needs worked examples or a second pass to be able to apply the ideas", 3.5},
		{"Needs deliberate practice or experimentation to internalise", 6.0},
		{"Needs extended study and hands-on work over multiple sessions", 12.0},
	}
}

func descriptions(levels []level) []string {
	out := make([]string, len(levels))
	for i, l := range levels {
		out[i] = l.description
	}
	return out
}
