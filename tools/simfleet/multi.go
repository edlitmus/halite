package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// A fleet larger than one driver host can hold is several `run
// -no-submit` processes, one `submit`, and a `merge` afterwards. Before
// these existed, each driver submitted its own job and timed only its own
// nodes, so four drivers gave four quarter-sized jobs rather than one job
// to the whole fleet (limit L1 of docs/simfleet-aws-plan.md), and no
// SPEC 30 row above one driver's size could be measured at all.
//
//	driver A: simfleet run -root DIR -hub H -first 1     -n 5000 -no-submit -out a.json
//	driver B: simfleet run -root DIR -hub H -first 5001  -n 5000 -no-submit -out b.json
//	observer: simfleet submit -root DIR -hub H -fun test.ping -out ping.json
//	(stop the drivers, or let -hold run out)
//	          simfleet merge -submit ping.json a.json b.json
//
// merge compares a driver's clock with the submitting host's. That is
// only as good as the hosts' clock agreement, which merge cannot see; the
// run kit records each host's offset before and after, and a result is
// worth what those offsets say it is.

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
}

func operatorClient(root, hub, serverName string, ca *x509.Certificate) (*transport.Client, error) {
	f := &fleet{hubURL: "https://" + hub, serverName: serverName, ca: ca}
	return f.operator(root)
}

func submitCmd(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	root := fs.String("root", "", "the directory `simfleet setup` wrote")
	hub := fs.String("hub", "127.0.0.1:4510", "the hub's address")
	serverName := fs.String("server-name", "localhost", "the name to verify in the hub's certificate")
	prefix := fs.String("prefix", "sim", "the node id prefix setup used")
	fun := fs.String("fun", "test.ping", "the function to send to every simulated node")
	out := fs.String("out", "", "write when the job was sent and accepted, as JSON, here")
	batch := addBatchFlags(fs)
	_ = fs.Parse(args)
	if *root == "" || *out == "" {
		return errors.New("-root and -out are required")
	}
	if err := batch.check(); err != nil {
		return err
	}
	ca, err := pki.Files{Dir: filepath.Join(*root, "pki")}.ReadCert(pki.CACertFile)
	if err != nil {
		return err
	}
	op, err := operatorClient(*root, *hub, *serverName, ca)
	if err != nil {
		return err
	}
	target := *prefix + "-*"
	start := time.Now()
	resp, err := op.Submit(context.Background(), batch.request(target, *fun))
	end := time.Now()
	if err != nil {
		return fmt.Errorf("submitting %s: %w", *fun, err)
	}
	sub := submitFile{JID: resp.JID, Fun: *fun, Target: target, Matched: len(resp.Nodes), Absent: len(resp.Absent),
		SubmitStart: start, SubmitEnd: end, Batch: resp.Batch}
	b, err := json.MarshalIndent(sub, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.Write(*out, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: jid %s, %d nodes matched, %d absent, the submit took %s\n",
		*fun, resp.JID, sub.Matched, sub.Absent, end.Sub(start).Round(time.Millisecond))
	return nil
}

func mergeCmd(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ExitOnError)
	subPath := fs.String("submit", "", "the file `simfleet submit -out` wrote")
	jsonOut := fs.String("json", "", "also append the result, as one JSON line, to this file")
	_ = fs.Parse(args)
	if *subPath == "" || fs.NArg() == 0 {
		return errors.New("usage: simfleet merge -submit FILE DRIVER.json...")
	}
	var sub submitFile
	if err := readJSON(*subPath, &sub); err != nil {
		return err
	}
	var drivers []driverFile
	for _, p := range fs.Args() {
		var d driverFile
		if err := readJSON(p, &d); err != nil {
			return err
		}
		drivers = append(drivers, d)
	}
	res, err := merge(sub, drivers)
	if err != nil {
		return err
	}
	var connected int64
	for _, d := range drivers {
		connected += d.Connected
	}
	fmt.Printf("%d drivers, %d of %d simulated nodes connected\n", len(drivers), connected, res.Expected)
	res.print(os.Stdout)
	return writeJSONLine(*jsonOut, res)
}

// merge puts several drivers' views of one job together. Every node a
// driver ran is expected to have the job, whether or not the driver's
// file mentions it: a driver that never saw the job is a driver whose
// nodes never received it, not one to leave out.
func merge(sub submitFile, drivers []driverFile) (result, error) {
	seen := map[string]string{}
	var received, returned []int64
	var errs int64
	expected := 0
	for _, d := range drivers {
		expected += d.N
		var job *driverJob
		for i := range d.Jobs {
			if d.Jobs[i].JID == sub.JID {
				job = &d.Jobs[i]
			}
		}
		if job == nil {
			received = append(received, make([]int64, d.N)...)
			returned = append(returned, make([]int64, d.N)...)
			continue
		}
		if len(job.Sims) != d.N {
			return result{}, fmt.Errorf("driver %s: %d nodes in job %s, but it ran %d", d.Driver, len(job.Sims), sub.JID, d.N)
		}
		for _, s := range job.Sims {
			// Two drivers running the same identity would each hold a
			// stream for it, and the hub keeps one; the other's numbers
			// would be a node that was never there.
			if other, dup := seen[s.ID]; dup {
				return result{}, fmt.Errorf("%s is in both driver %s and driver %s; give each driver its own -first", s.ID, other, d.Driver)
			}
			seen[s.ID] = d.Driver
			received = append(received, s.Received)
			returned = append(returned, s.Returned)
		}
		errs += job.ReturnErrors
	}
	return summarize(sub, expected, received, returned, errs, len(drivers)), nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
