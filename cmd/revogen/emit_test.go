package main

import "testing"

func TestRenderPathExpr(t *testing.T) {
	cases := []struct {
		name   string
		tmpl   string
		params []*PathParam
		want   string
	}{
		{
			name:   "no params",
			tmpl:   "/accounts",
			params: nil,
			want:   `"accounts"`,
		},
		{
			name:   "snake-case match",
			tmpl:   "/accounts/{account_id}",
			params: []*PathParam{{Name: "account_id", GoName: "accountID"}},
			want:   `"accounts/" + url.PathEscape(accountID)`,
		},
		{
			name:   "camel in template, snake in parameters",
			tmpl:   "/card-invitations/{cardInvitationId}/cancel",
			params: []*PathParam{{Name: "card_invitation_id", GoName: "cardInvitationID"}},
			want:   `"card-invitations/" + url.PathEscape(cardInvitationID) + "/cancel"`,
		},
		{
			name:   "multi params",
			tmpl:   "/expenses/{expense_id}/receipts/{receipt_id}/content",
			params: []*PathParam{
				{Name: "expense_id", GoName: "expenseID"},
				{Name: "receipt_id", GoName: "receiptID"},
			},
			want: `"expenses/" + url.PathEscape(expenseID) + "/receipts/" + url.PathEscape(receiptID) + "/content"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderPathExpr(c.tmpl, c.params); got != c.want {
				t.Errorf("renderPathExpr(%q) = %q\n want %q", c.tmpl, got, c.want)
			}
		})
	}
}
