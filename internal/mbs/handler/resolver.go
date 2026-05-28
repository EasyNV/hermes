package handler

import (
	"context"

	"mbs-native/auth"
	"mbs-native/graphql"
)

// PhoneResolver is the small surface the handler needs from a graphql
// client to perform a live phone → thread_id resolve. Defining an
// interface lets tests inject a fake without spinning up a real
// graphql.Client (which needs an HTTP server + valid creds).
//
// One method per resolve operation. If a future resolver supports
// additional ops (e.g., batch resolve), expand here.
type PhoneResolver interface {
	// ResolvePhoneToThreadID hits BizInboxWhatsAppCustomerMutation
	// for (pageID, phone) and returns (customer_id, wec_mailbox_id, err).
	// phone is the normalized E.164 form (minus leading +).
	ResolvePhoneToThreadID(ctx context.Context, pageID, phone string) (customerID, wecMailboxID string, err error)
}

// PhoneResolverFactory builds a PhoneResolver from a decrypted Creds.
// Handler invokes per call (cheap; just wraps an HTTP client). Tests
// inject a closure returning a fake; production uses
// defaultResolverFactory.
type PhoneResolverFactory func(creds *auth.Creds) (PhoneResolver, error)

// defaultResolverFactory wraps graphql.New so we don't have to import
// graphql at the handler call site. New is the only production path.
func defaultResolverFactory(creds *auth.Creds) (PhoneResolver, error) {
	gc, err := graphql.New(creds)
	if err != nil {
		return nil, err
	}
	return &graphqlAdapter{gc: gc}, nil
}

// graphqlAdapter adapts *graphql.Client to the local PhoneResolver
// interface. Stays at one method; the handler only uses
// ResolvePhoneToThreadID. If the handler grows to call other graphql
// methods, expand both this adapter and the PhoneResolver interface.
type graphqlAdapter struct {
	gc *graphql.Client
}

func (g *graphqlAdapter) ResolvePhoneToThreadID(ctx context.Context, pageID, phone string) (string, string, error) {
	return g.gc.ResolvePhoneToThreadID(ctx, pageID, phone)
}
