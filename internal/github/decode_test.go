package github

import "testing"

// TestHumanCommentedAt covers the rule the field's name has always claimed:
// somebody who is not the author, and not a bot.
func TestHumanCommentedAt(t *testing.T) {
	type comment struct {
		at, login, kind string
	}
	type review struct {
		at, login, kind, state string
	}

	tests := []struct {
		name     string
		author   string
		comments []comment
		reviews  []review
		want     string
	}{
		{
			name:     "somebody else",
			author:   "author",
			comments: []comment{{"2026-08-02T11:00:00Z", "reviewer", "User"}},
			want:     "2026-08-02T11:00:00Z",
		},
		{
			// The reported case: a pull request opened at 19:28, marked
			// human-commented at 19:29 by the author's own automation.
			name:   "a bot replying to itself",
			author: "author",
			comments: []comment{
				{"2026-08-02T19:29:00Z", "helper", botType},
				{"2026-08-02T19:31:00Z", "helper", botType},
				{"2026-08-02T19:33:00Z", "helper", botType},
			},
			want: "",
		},
		{
			name:   "the author talking to themselves",
			author: "author",
			comments: []comment{
				{"2026-08-02T11:00:00Z", "author", "User"},
				{"2026-08-02T12:00:00Z", "author", "User"},
			},
			want: "",
		},
		{
			// The reason the window is more than one node: the newest comment
			// is noise and the one that counts is underneath it.
			name:   "noise on top of a real comment",
			author: "author",
			comments: []comment{
				{"2026-08-02T11:00:00Z", "reviewer", "User"},
				{"2026-08-02T12:00:00Z", "author", "User"},
				{"2026-08-02T13:00:00Z", "helper", botType},
			},
			want: "2026-08-02T11:00:00Z",
		},
		{
			name:    "a review counts",
			author:  "author",
			reviews: []review{{"2026-08-02T11:00:00Z", "reviewer", "User", "APPROVED"}},
			want:    "2026-08-02T11:00:00Z",
		},
		{
			name:     "the newest of either kind wins",
			author:   "author",
			comments: []comment{{"2026-08-02T11:00:00Z", "reviewer", "User"}},
			reviews:  []review{{"2026-08-02T14:00:00Z", "other", "User", "COMMENTED"}},
			want:     "2026-08-02T14:00:00Z",
		},
		{
			name:     "an older review does not override a newer comment",
			author:   "author",
			comments: []comment{{"2026-08-02T14:00:00Z", "reviewer", "User"}},
			reviews:  []review{{"2026-08-02T11:00:00Z", "other", "User", "APPROVED"}},
			want:     "2026-08-02T14:00:00Z",
		},
		{
			// Nobody else can see a pending review, so it is not evidence
			// that anybody read anything.
			name:    "a pending review does not count",
			author:  "author",
			reviews: []review{{"2026-08-02T11:00:00Z", "reviewer", "User", pendingReview}},
			want:    "",
		},
		{
			name:    "an author's own review",
			author:  "author",
			reviews: []review{{"2026-08-02T11:00:00Z", "author", "User", "COMMENTED"}},
			want:    "",
		},
		{
			// A deleted account cannot be checked against the author, so it is
			// guessed in the direction that freezes: an unnecessary freeze
			// costs a commit, a missing one rewrites what somebody has read.
			name:     "an actor GitHub will not name",
			author:   "author",
			comments: []comment{{"2026-08-02T11:00:00Z", "", ""}},
			want:     "2026-08-02T11:00:00Z",
		},
		{
			name:   "nothing at all",
			author: "author",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p wirePullRequest
			p.Author = &wireActor{Login: tt.author, TypeName: "User"}
			for _, c := range tt.comments {
				node := struct {
					CreatedAt string     `json:"createdAt"`
					Author    *wireActor `json:"author"`
				}{CreatedAt: c.at}
				if c.login != "" || c.kind != "" {
					node.Author = &wireActor{Login: c.login, TypeName: c.kind}
				}
				p.Comments.Nodes = append(p.Comments.Nodes, node)
			}
			for _, r := range tt.reviews {
				p.Reviews.Nodes = append(p.Reviews.Nodes, struct {
					CreatedAt string     `json:"createdAt"`
					State     string     `json:"state"`
					Author    *wireActor `json:"author"`
				}{CreatedAt: r.at, State: r.state,
					Author: &wireActor{Login: r.login, TypeName: r.kind}})
			}

			if got := humanCommentedAt(&p); got != tt.want {
				t.Errorf("humanCommentedAt() = %q, want %q", got, tt.want)
			}
		})
	}
}
