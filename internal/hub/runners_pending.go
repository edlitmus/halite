package hub

// registerPendingRunners declares the rest of the SPEC 19.2 inventory.
//
// They are registered rather than omitted so that `runner list` shows
// the whole inventory and a call to one of them says when it arrives.
// The alternative — leaving a name out until it is built — makes
// "orchestration is not written yet" and "you have mistyped
// state.orchestrate" the same message, and an operator cannot tell
// which from the terminal.
func registerPendingRunners(r *Runners) {
	pending := func(module, function, doc, when string) RunnerModule {
		return RunnerModule{Sig: runnerSig(module, function, doc, "19.2"), Pending: when}
	}

	const mine = "phase 3, with the mine (SPEC section 19.5)"
	const queues = "phase 3, with the durable work queue (SPEC section 19.4)"
	const notify = "phase 4, with the API and its integrations (SPEC section 32)"

	r.Add(
		pending("mine", "get", "Read what nodes have published to the mine.", mine),
		pending("mine", "update", "Ask nodes to refresh their mine entries.", mine),
		pending("mine", "flush", "Drop every mine entry for a node.", mine),
		pending("mine", "delete", "Drop one mine function's entry for a node.", mine),
		pending("mine", "valid", "The mine functions a node is configured to publish.", mine),

		pending("queue", "insert", "Add an item to a durable hub queue.", queues),
		pending("queue", "delete", "Remove an item from a queue.", queues),
		pending("queue", "list_queues", "The queues this hub holds.", queues),
		pending("queue", "list_length", "How many items a queue holds.", queues),
		pending("queue", "list_items", "The items in a queue.", queues),
		pending("queue", "process_queue", "Take items off a queue and run them.", queues),

		// `net` aggregates what nodes have published rather than
		// talking to network devices, per SPEC 19.2, so it waits for
		// the mine that holds the data.
		pending("net", "find", "Find which node owns an address or a MAC.", mine),
		pending("net", "interfaces", "The fleet's interfaces, from published data.", mine),
		pending("net", "arp", "The fleet's ARP tables, from published data.", mine),

		pending("smtp", "send", "Send mail, over net/smtp with STARTTLS or implicit TLS.", notify),
		pending("slack", "post", "Post to a webhook target through the signed-webhook runner.", notify),
		pending("http", "query", "Make an HTTP request from the hub.", notify),
	)
}
