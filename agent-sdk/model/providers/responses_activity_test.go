package providers

import "testing"

func TestResponsesSSEHasSemanticActivity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want bool
	}{
		{name: "created", data: "{\"type\":\"response.created\"}", want: false},
		{name: "metadata", data: "{\"type\":\"response.metadata\"}", want: false},
		{name: "heartbeat", data: "{\"type\":\"heartbeat\"}", want: false},
		{name: "empty delta", data: "{\"type\":\"response.output_text.delta\",\"delta\":\"\"}", want: false},
		{name: "text delta", data: "{\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}", want: true},
		{name: "tool progress", data: "{\"type\":\"response.web_search_call.searching\"}", want: true},
		{name: "completed", data: "{\"type\":\"response.completed\"}", want: true},
		{name: "unknown event", data: "{\"type\":\"response.future_progress\"}", want: true},
		{name: "malformed", data: "{", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := responsesSSEHasSemanticActivity([]byte(tt.data)); got != tt.want {
				t.Fatalf("responsesSSEHasSemanticActivity(%s) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}
