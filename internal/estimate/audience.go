package estimate

// Audience is a professional reader profile with a baseline silent-reading speed.
type Audience struct {
	ID             string
	Name           string
	Description    string
	WordsPerMinute float64
}

// Audiences returns the built-in reader profiles, from least to most specialised.
func Audiences() []Audience {
	return []Audience{
		{ID: "esl", Name: "Non-native professional", Description: "an experienced professional in the text's field who reads English as a second language at an intermediate-to-advanced level", WordsPerMinute: 170},
		{ID: "crossfn", Name: "Cross-functional peer", Description: "an experienced professional from an adjacent role, such as product, design or management, who works outside the text's field", WordsPerMinute: 238},
		{ID: "junior", Name: "Junior professional", Description: "an early-career professional in the text's field who knows the fundamentals but not advanced or organisation-specific topics", WordsPerMinute: 220},
		{ID: "senior", Name: "Senior practitioner", Description: "a senior practitioner with years of hands-on experience in the text's field", WordsPerMinute: 260},
		{ID: "expert", Name: "Domain expert", Description: "a recognised expert in the text's exact subject who knows its specialised vocabulary and literature", WordsPerMinute: 280},
	}
}
