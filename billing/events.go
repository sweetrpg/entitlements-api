package billing

// Minimal views of the Stripe objects the handlers read. They are declared here instead of
// using the SDK's types so a Stripe API-version change that moves a field cannot break decoding
// of the fields we do not use. Newer API versions move the subscription link on an invoice under
// parent.subscription_details; both locations are read.

type checkoutSession struct {
	ID                string            `json:"id"`
	Mode              string            `json:"mode"`
	PaymentStatus     string            `json:"payment_status"`
	Customer          string            `json:"customer"`
	Subscription      string            `json:"subscription"`
	PaymentIntent     string            `json:"payment_intent"`
	ClientReferenceID string            `json:"client_reference_id"`
	Metadata          map[string]string `json:"metadata"`
}

type subscriptionDetails struct {
	Subscription string            `json:"subscription"`
	Metadata     map[string]string `json:"metadata"`
}

type invoice struct {
	ID                  string               `json:"id"`
	Subscription        string               `json:"subscription"`
	PaymentIntent       string               `json:"payment_intent"`
	Charge              string               `json:"charge"`
	SubscriptionDetails *subscriptionDetails `json:"subscription_details"`
	Parent              *struct {
		SubscriptionDetails *subscriptionDetails `json:"subscription_details"`
	} `json:"parent"`
	Lines struct {
		Data []struct {
			Period struct {
				End int64 `json:"end"`
			} `json:"period"`
		} `json:"data"`
	} `json:"lines"`
}

// details returns the subscription link and metadata from whichever location carries it.
func (i invoice) details() (subscription string, metadata map[string]string) {
	candidates := []*subscriptionDetails{i.SubscriptionDetails}
	if i.Parent != nil {
		candidates = append(candidates, i.Parent.SubscriptionDetails)
	}
	subscription = i.Subscription
	for _, d := range candidates {
		if d == nil {
			continue
		}
		if subscription == "" {
			subscription = d.Subscription
		}
		if metadata == nil {
			metadata = d.Metadata
		}
	}
	return subscription, metadata
}

// periodEnd is the latest period end across the invoice's lines, 0 if none.
func (i invoice) periodEnd() int64 {
	var end int64
	for _, l := range i.Lines.Data {
		end = max(end, l.Period.End)
	}
	return end
}

type subscription struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type charge struct {
	ID            string `json:"id"`
	PaymentIntent string `json:"payment_intent"`
	Invoice       string `json:"invoice"`
	Refunded      bool   `json:"refunded"`
}

type dispute struct {
	ID            string `json:"id"`
	Charge        string `json:"charge"`
	PaymentIntent string `json:"payment_intent"`
}
