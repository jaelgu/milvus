// Licensed to the LF AI & Data foundation under one
// or more contributor license agreements. See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership. The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package datanode

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	memkv "github.com/milvus-io/milvus/internal/kv/mem"
	"github.com/milvus-io/milvus/internal/proto/internalpb"
	"github.com/milvus-io/milvus/internal/util/retry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

type emptyFlushTask struct{}

func (t *emptyFlushTask) flushInsertData() error {
	return nil
}

func (t *emptyFlushTask) flushDeleteData() error {
	return nil
}

type errFlushTask struct{}

func (t *errFlushTask) flushInsertData() error {
	return errors.New("mocked error")
}

func (t *errFlushTask) flushDeleteData() error {
	return errors.New("mocked error")
}

func TestOrderFlushQueue_Execute(t *testing.T) {
	counter := atomic.Int64{}
	finish := sync.WaitGroup{}

	size := 1000
	finish.Add(size)
	q := newOrderFlushQueue(1, func(*segmentFlushPack) {
		counter.Inc()
		finish.Done()
	})

	q.init()
	ids := make([][]byte, 0, size)
	for i := 0; i < size; i++ {
		id := make([]byte, 10)
		rand.Read(id)
		ids = append(ids, id)
	}

	wg := sync.WaitGroup{}
	wg.Add(2 * size)
	for i := 0; i < size; i++ {
		go func(id []byte) {
			q.enqueueDelFlush(&emptyFlushTask{}, &DelDataBuf{}, &internalpb.MsgPosition{
				MsgID: id,
			})
			wg.Done()
		}(ids[i])
		go func(id []byte) {
			q.enqueueInsertFlush(&emptyFlushTask{}, map[UniqueID]string{}, map[UniqueID]string{}, false, false, &internalpb.MsgPosition{
				MsgID: id,
			})
			wg.Done()
		}(ids[i])
	}
	wg.Wait()
	finish.Wait()

	assert.EqualValues(t, size, counter.Load())
}
func TestOrderFlushQueue_Order(t *testing.T) {
	counter := atomic.Int64{}
	finish := sync.WaitGroup{}

	size := 1000
	finish.Add(size)
	resultList := make([][]byte, 0, size)
	q := newOrderFlushQueue(1, func(pack *segmentFlushPack) {
		counter.Inc()
		resultList = append(resultList, pack.pos.MsgID)
		finish.Done()
	})

	q.init()
	ids := make([][]byte, 0, size)
	for i := 0; i < size; i++ {
		id := make([]byte, 10)
		rand.Read(id)
		ids = append(ids, id)
	}

	wg := sync.WaitGroup{}
	wg.Add(size)
	for i := 0; i < size; i++ {
		q.enqueueDelFlush(&emptyFlushTask{}, &DelDataBuf{}, &internalpb.MsgPosition{
			MsgID: ids[i],
		})
		q.enqueueInsertFlush(&emptyFlushTask{}, map[UniqueID]string{}, map[UniqueID]string{}, false, false, &internalpb.MsgPosition{
			MsgID: ids[i],
		})
		wg.Done()
	}
	wg.Wait()
	finish.Wait()

	assert.EqualValues(t, size, counter.Load())

	require.Equal(t, size, len(resultList))
	for i := 0; i < size; i++ {
		assert.EqualValues(t, ids[i], resultList[i])
	}
}

func TestRendezvousFlushManager(t *testing.T) {
	kv := memkv.NewMemoryKV()

	size := 1000
	var counter atomic.Int64
	finish := sync.WaitGroup{}
	finish.Add(size)
	m := NewRendezvousFlushManager(&allocator{}, kv, newMockReplica(), func(pack *segmentFlushPack) {
		counter.Inc()
		finish.Done()
	})

	ids := make([][]byte, 0, size)
	for i := 0; i < size; i++ {
		id := make([]byte, 10)
		rand.Read(id)
		ids = append(ids, id)
	}

	wg := sync.WaitGroup{}
	wg.Add(size)
	for i := 0; i < size; i++ {
		m.flushDelData(nil, 1, &internalpb.MsgPosition{
			MsgID: ids[i],
		})
		m.flushBufferData(nil, 1, true, false, &internalpb.MsgPosition{
			MsgID: ids[i],
		})
		wg.Done()
	}
	wg.Wait()
	finish.Wait()

	assert.EqualValues(t, size, counter.Load())
}

func TestRendezvousFlushManager_Inject(t *testing.T) {
	kv := memkv.NewMemoryKV()

	size := 1000
	var counter atomic.Int64
	finish := sync.WaitGroup{}
	finish.Add(size)
	packs := make([]*segmentFlushPack, 0, size+1)
	m := NewRendezvousFlushManager(&allocator{}, kv, newMockReplica(), func(pack *segmentFlushPack) {
		packs = append(packs, pack)
		counter.Inc()
		finish.Done()
	})

	injected := make(chan struct{})
	injectOver := make(chan bool)
	m.injectFlush(taskInjection{
		injected:   injected,
		injectOver: injectOver,
		postInjection: func(*segmentFlushPack) {
		},
	}, 1)
	<-injected
	injectOver <- true

	ids := make([][]byte, 0, size)
	for i := 0; i < size; i++ {
		id := make([]byte, 10)
		rand.Read(id)
		ids = append(ids, id)
	}

	wg := sync.WaitGroup{}
	wg.Add(size)
	for i := 0; i < size; i++ {
		m.flushDelData(nil, 1, &internalpb.MsgPosition{
			MsgID: ids[i],
		})
		m.flushBufferData(nil, 1, true, false, &internalpb.MsgPosition{
			MsgID: ids[i],
		})
		wg.Done()
	}
	wg.Wait()
	finish.Wait()

	assert.EqualValues(t, size, counter.Load())

	finish.Add(1)
	id := make([]byte, 10)
	rand.Read(id)
	m.flushBufferData(nil, 2, true, false, &internalpb.MsgPosition{
		MsgID: id,
	})

	m.injectFlush(taskInjection{
		injected:   injected,
		injectOver: injectOver,
		postInjection: func(pack *segmentFlushPack) {
			pack.segmentID = 3
		},
	}, 2)

	go func() {
		<-injected
		injectOver <- true
	}()
	m.flushDelData(nil, 2, &internalpb.MsgPosition{
		MsgID: id,
	})

	finish.Wait()
	assert.EqualValues(t, size+1, counter.Load())
	assert.EqualValues(t, 3, packs[size].segmentID)

	finish.Add(1)
	rand.Read(id)
	m.flushBufferData(nil, 2, false, false, &internalpb.MsgPosition{
		MsgID: id,
	})
	m.flushDelData(nil, 2, &internalpb.MsgPosition{
		MsgID: id,
	})
	finish.Wait()
	assert.EqualValues(t, size+2, counter.Load())
	assert.EqualValues(t, 3, packs[size+1].segmentID)

}

func TestRendezvousFlushManager_getSegmentMeta(t *testing.T) {
	memkv := memkv.NewMemoryKV()
	replica := newMockReplica()
	fm := NewRendezvousFlushManager(NewAllocatorFactory(), memkv, replica, func(*segmentFlushPack) {
	})

	// non exists segment
	_, _, _, err := fm.getSegmentMeta(-1, &internalpb.MsgPosition{})
	assert.Error(t, err)

	replica.newSegments[-1] = &Segment{}
	replica.newSegments[1] = &Segment{}

	// injected get part/coll id error
	_, _, _, err = fm.getSegmentMeta(-1, &internalpb.MsgPosition{})
	assert.Error(t, err)
	// injected get schema  error
	_, _, _, err = fm.getSegmentMeta(1, &internalpb.MsgPosition{})
	assert.Error(t, err)
}

func TestFlushNotifyFunc(t *testing.T) {
	//	replica :=
	//	rcf := &RootCoordFactory{}
	ctx := context.Background()
	rcf := &RootCoordFactory{}

	replica, err := newReplica(ctx, rcf, 1)
	require.NoError(t, err)

	dataCoord := &DataCoordFactory{}
	flushingCache := newCache()
	dsService := &dataSyncService{
		collectionID:     1,
		replica:          replica,
		dataCoord:        dataCoord,
		flushingSegCache: flushingCache,
	}
	notifyFunc := flushNotifyFunc(dsService, retry.Attempts(1))

	t.Run("normal run", func(t *testing.T) {
		assert.NotPanics(t, func() {
			notifyFunc(&segmentFlushPack{
				insertLogs: map[UniqueID]string{1: "/dev/test/id"},
				statsLogs:  map[UniqueID]string{1: "/dev/test/id-stats"},
				deltaLogs:  []*DelDataBuf{{filePath: "/dev/test/del"}},
				flushed:    true,
			})
		})
	})

	t.Run("pack has error", func(t *testing.T) {
		assert.Panics(t, func() {
			notifyFunc(&segmentFlushPack{
				err: errors.New("mocked pack error"),
			})
		})
	})

	t.Run("datacoord Save fails", func(t *testing.T) {
		dataCoord.SaveBinlogPathNotSuccess = true
		assert.Panics(t, func() {
			notifyFunc(&segmentFlushPack{})
		})
	})

	t.Run("datacoord call error", func(t *testing.T) {
		dataCoord.SaveBinlogPathError = true
		assert.Panics(t, func() {
			notifyFunc(&segmentFlushPack{})
		})
	})
}
