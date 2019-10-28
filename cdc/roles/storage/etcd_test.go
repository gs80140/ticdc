// Copyright 2019 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/embed"
	"github.com/pingcap/check"
	"github.com/pingcap/tidb-cdc/cdc/kv"
	"github.com/pingcap/tidb-cdc/cdc/model"
	"github.com/pingcap/tidb-cdc/pkg/etcd"
)

func TestSuite(t *testing.T) { check.TestingT(t) }

type etcdSuite struct {
	e         *embed.Etcd
	clientURL *url.URL
	client    *clientv3.Client
}

var _ = check.Suite(&etcdSuite{})

func (s *etcdSuite) SetUpTest(c *check.C) {
	dir := c.MkDir()
	var err error
	s.clientURL, s.e, err = etcd.SetupEmbedEtcd(dir)
	c.Assert(err, check.IsNil)
	s.client, err = clientv3.New(clientv3.Config{
		Endpoints:   []string{s.clientURL.String()},
		DialTimeout: 3 * time.Second,
	})
	c.Assert(err, check.IsNil)
	go func() {
		c.Log(<-s.e.Err())
	}()
}

func (s *etcdSuite) TearDownTest(c *check.C) {
	s.e.Close()
}

func (s *etcdSuite) TestInfoReader(c *check.C) {
	var (
		info1 = map[model.CaptureID]*model.SubChangeFeedInfo{
			"capture1": {
				CheckPointTS: 1000,
				ResolvedTS:   1024,
				TableInfos: []*model.ProcessTableInfo{
					{ID: 1000, StartTS: 0},
					{ID: 1001, StartTS: 100},
				},
			},
			"capture2": {
				CheckPointTS: 1000,
				ResolvedTS:   1500,
				TableInfos: []*model.ProcessTableInfo{
					{ID: 1002, StartTS: 150},
					{ID: 1003, StartTS: 200},
				},
			},
		}
		err error
	)
	testCases := []struct {
		ids    []string
		pinfos map[string]model.ProcessorsInfos
	}{
		{ids: nil, pinfos: nil},
		{ids: []string{"changefeed1"}, pinfos: map[string]model.ProcessorsInfos{"changefeed1": info1}},
		{ids: []string{"changefeed1", "changefeed2"}, pinfos: map[string]model.ProcessorsInfos{"changefeed1": info1, "changefeed2": info1}},
	}

	rw := NewChangeFeedInfoEtcdRWriter(s.client)
	for _, tc := range testCases {
		_, err = s.client.Delete(context.Background(), kv.GetEtcdKeyChangeFeedList(), clientv3.WithPrefix())
		c.Assert(err, check.IsNil)
		for _, changefeedID := range tc.ids {
			_, err = s.client.Delete(context.Background(), kv.GetEtcdKeySubChangeFeedList(changefeedID), clientv3.WithPrefix())
			c.Assert(err, check.IsNil)
		}
		for i := 0; i < len(tc.ids); i++ {
			changefeedID := tc.ids[i]
			_, err = s.client.Put(context.Background(), kv.GetEtcdKeyChangeFeedConfig(changefeedID), "")
			c.Assert(err, check.IsNil)
			for captureID, cinfo := range tc.pinfos[changefeedID] {
				_, err = s.client.Put(context.Background(), kv.GetEtcdKeySubChangeFeed(changefeedID, captureID), cinfo.Marshal())
				c.Assert(err, check.IsNil)
			}
		}
		pinfos, err := rw.Read(context.Background())
		c.Assert(err, check.IsNil)
		c.Assert(len(pinfos), check.Equals, len(tc.ids))
		for _, changefeedID := range tc.ids {
			c.Assert(pinfos[changefeedID], check.DeepEquals, tc.pinfos[changefeedID])
		}
	}
}

func (s *etcdSuite) TestInfoWriter(c *check.C) {
	var (
		info1 = &model.ChangeFeedInfo{
			ResolvedTS:   2200,
			CheckpointTS: 2000,
		}
		info2 = &model.ChangeFeedInfo{
			ResolvedTS:   2600,
			CheckpointTS: 2500,
		}
		err error
	)
	largeTxnInfo := make(map[string]*model.ChangeFeedInfo, embed.DefaultMaxTxnOps+1)
	for i := 0; i < int(embed.DefaultMaxTxnOps)+1; i++ {
		changefeedID := fmt.Sprintf("changefeed%d", i+1)
		largeTxnInfo[changefeedID] = info1
	}
	testCases := []struct {
		infos map[model.ChangeFeedID]*model.ChangeFeedInfo
	}{
		{infos: nil},
		{infos: map[string]*model.ChangeFeedInfo{"changefeed1": info1}},
		{infos: map[string]*model.ChangeFeedInfo{"changefeed1": info1, "changefeed2": info2}},
		{infos: largeTxnInfo},
	}

	rw := NewChangeFeedInfoEtcdRWriter(s.client)
	for _, tc := range testCases {
		for changefeedID := range tc.infos {
			_, err = s.client.Delete(context.Background(), kv.GetEtcdKeyChangeFeedStatus(changefeedID))
			c.Assert(err, check.IsNil)
		}

		err = rw.Write(context.Background(), tc.infos)
		c.Assert(err, check.IsNil)

		for changefeedID, info := range tc.infos {
			resp, err := s.client.Get(context.Background(), kv.GetEtcdKeyChangeFeedStatus(changefeedID))
			c.Assert(err, check.IsNil)
			c.Assert(resp.Count, check.Equals, int64(1))
			infoStr, err := info.Marshal()
			c.Assert(err, check.IsNil)
			c.Assert(string(resp.Kvs[0].Value), check.Equals, infoStr)
		}
	}
}