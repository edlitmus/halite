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

	// The mine is built; these three read from it and are not. Naming
	// the subsystem rather than a phase is what keeps the message true
	// once the phase lands and the function still does not exist.
	const mine = "the mine it would read is built; this reader is not (SPEC section 19.5)"
	const queues = "it needs the durable work queue of SPEC section 19.4"
	const notify = "the hub has no outbound notification runner; the smtp, syslog, and " +
		"webhook returners send from the return path instead (SPEC section 20.3)"

	r.Add(
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
