package xwal

import (
	"fmt"
	"sync"
	"testing"
)

func TestRecordsFromConcurrentAppend(t *testing.T) {
	x, _ := triune(t, t.TempDir())
	defer x.Close()
	for i := uint64(1); i <= 8; i++ {
		if _, err := x.AppendMain([]byte(fmt.Sprintf(`{"ir":%d}`, i)), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := x.Append("translations", i, []byte(fmt.Sprintf(`{"wire":%d}`, i)), nil); err != nil {
			t.Fatal(err)
		}
	}

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(9); i <= 48; i++ {
			if _, err := x.AppendMain([]byte(fmt.Sprintf(`{"ir":%d}`, i)), nil); err != nil {
				errs <- err
				return
			}
			if _, err := x.Append("translations", i, []byte(fmt.Sprintf(`{"wire":%d}`, i)), nil); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 80 {
			records, err := x.RecordsFrom("translations", 1, 0)
			if err != nil {
				errs <- err
				return
			}
			for i := 1; i < len(records); i++ {
				if records[i].MainLT <= records[i-1].MainLT || records[i].ChannelLT <= records[i-1].ChannelLT {
					errs <- fmt.Errorf("records not ordered: %+v then %+v", records[i-1], records[i])
					return
				}
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
