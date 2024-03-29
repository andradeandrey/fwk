package fwk

import (
	"fmt"
)

type workercontrol struct {
	evts chan int64
	quit chan struct{}
	done chan struct{}
	errc chan error
}

type worker struct {
	slot  int
	keys  []string
	store datastore
	ctxs  []context
	msg   msgstream

	evts <-chan int64
	quit <-chan struct{}
	done chan<- struct{}
	errc chan<- error
}

func newWorker(i int, app *appmgr, ctrl *workercontrol) *worker {
	wrk := &worker{
		slot:  i,
		keys:  app.dflow.keys(),
		store: *app.store,
		ctxs:  make([]context, len(app.tsks)),
		msg:   NewMsgStream(fmt.Sprintf("%s-worker-%03d", app.name, i), app.msg.lvl, nil),
		evts:  ctrl.evts,
		quit:  ctrl.quit,
		done:  ctrl.done,
		errc:  ctrl.errc,
	}
	wrk.store.store = make(map[string]achan, len(wrk.keys))
	for j, tsk := range app.tsks {
		wrk.ctxs[j] = context{
			id:    -1,
			slot:  i,
			store: &wrk.store,
			msg:   NewMsgStream(tsk.Name(), app.msg.lvl, nil),
			mgr:   nil, // nobody's supposed to access mgr's state during event-loop
		}
	}

	go wrk.run(app.tsks)

	return wrk
}

func (wrk *worker) run(tsks []Task) {
	defer func() {
		wrk.done <- struct{}{}
	}()

	for {
		select {
		case ievt, ok := <-wrk.evts:
			if !ok {
				return
			}
			wrk.msg.Debugf(">>> running evt=%d...\n", ievt)
			err := wrk.store.reset(wrk.keys)
			if err != nil {
				wrk.errc <- err
				return
			}

			evt := taskrunner{
				ievt: ievt,
				errc: make(chan error, len(tsks)),
				quit: make(chan struct{}),
			}
			for i, tsk := range tsks {
				go evt.run(i, wrk.ctxs[i], tsk)
			}
			ndone := 0
		errloop:
			for {
				select {
				case err, ok := <-evt.errc:
					if !ok {
						return
					}
					ndone++
					if err != nil {
						close(evt.quit)
						wrk.store.close()
						wrk.msg.flush()
						wrk.errc <- err
						return
					}
					if ndone == len(tsks) {
						break errloop
					}
				case <-wrk.quit:
					wrk.store.close()
					close(evt.quit)
					wrk.msg.flush()
					return
				}
			}
			wrk.store.close()
			close(evt.quit)
			wrk.msg.flush()

		case <-wrk.quit:
			wrk.store.close()
			return
		}
	}
}

type taskrunner struct {
	errc chan error
	quit chan struct{}

	ievt int64
}

func (run taskrunner) run(i int, ctx context, tsk Task) {
	ctx.id = run.ievt
	select {
	case run.errc <- tsk.Process(ctx):
		// FIXME(sbinet) dont be so eager to flush...
		ctx.msg.flush()
	case <-run.quit:
		ctx.msg.flush()
	}
}
