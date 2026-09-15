package importer

// estimateCSVRecordMemory reserves string payload/copy allowance plus CSV string
// headers, converted interfaces, row slices and amortized batch backing arrays.
// Parser scratch, allocator size classes, stacks, SQL/driver copies and OS buffers
// are not a hard RSS bound; the operational guide describes those exclusions.
func estimateInterfaceRecordMemory(row []any) int64 {
	size := saturatingAdd(64, saturatingMultiply(int64(len(row)), 96))
	for _, v := range row {
		switch value := v.(type) {
		case string:
			size = saturatingAdd(size, saturatingMultiply(int64(len(value)), 2))
		case []byte:
			size = saturatingAdd(size, saturatingMultiply(int64(len(value)), 2))
		}
	}
	return size
}

func estimateCSVRecordMemory(row []string) int64 {
	size := saturatingAdd(64, saturatingMultiply(int64(len(row)), 96))
	for _, v := range row {
		size = saturatingAdd(size, saturatingMultiply(int64(len(v)), 2))
	}
	return size
}
