// +build !go1.6

package s3

import "github.com/flynn/flynn/Godeps/_workspace/src/github.com/aws/aws-sdk-go/aws/request"

func platformRequestHandlers(r *request.Request) {
}
