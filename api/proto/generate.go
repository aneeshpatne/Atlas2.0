//go:generate protoc -I . --go_out=../../gen --go_opt=paths=source_relative --go-grpc_out=../../gen --go-grpc_opt=paths=source_relative screen/v1/news_item.proto screen/v1/news_screen.proto

package proto
