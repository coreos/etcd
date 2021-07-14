package backend

import bolt "go.etcd.io/bbolt"

func DbFromBackendForTest(b Backend) *bolt.DB {
	return b.(*backend).db
}

func DefragLimitForTest() int {
	return defragLimit
}

func CommitsForTest(b Backend) int64 {
	return b.(*backend).Commits()
}
