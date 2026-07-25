package anthropic

import "testing"

func TestAPIErrorMessage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "typed message",
			status: 404,
			body:   `{"type":"error","error":{"type":"not_found_error","message":"model: claude-bogus-9"}}`,
			want:   "not_found_error: model: claude-bogus-9 (404)",
		},
		{
			name:   "message without type",
			status: 400,
			body:   `{"error":{"message":"bad thing"}}`,
			want:   "bad thing (400)",
		},
		{
			name:   "non-json body",
			status: 502,
			body:   "upstream boom",
			want:   "HTTP 502: upstream boom",
		},
		{
			name:   "empty body",
			status: 500,
			body:   "",
			want:   "HTTP 500",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := apiErrorMessage(tc.status, []byte(tc.body)); got != tc.want {
				t.Errorf("apiErrorMessage(%d, %q) = %q, want %q", tc.status, tc.body, got, tc.want)
			}
		})
	}
}
