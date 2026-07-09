// Package peertube is a small, focused client for uploading videos to a
// PeerTube instance (https://joinpeertube.org).
//
// It implements exactly what is needed to authenticate and publish a video:
//
//   - OAuth2 login (password grant) against /api/v1/users/token.
//   - Legacy single-request upload  (POST /api/v1/videos/upload).
//   - Resumable chunked upload      (POST/PUT /api/v1/videos/upload-resumable),
//     following the node-uploadx protocol used by PeerTube.
//
// The client is transport-agnostic: it talks to anything implementing [Doer]
// (which *http.Client satisfies), so it is trivial to unit test against an
// httptest.Server or an in-memory mock.
//
// Typical usage:
//
//	c, err := peertube.NewClient("https://peertube.example.org")
//	if err != nil {
//		return err
//	}
//	if _, err := c.Login(ctx, "user", "secret"); err != nil {
//		return err
//	}
//	f, err := os.Open("video.mp4")
//	if err != nil {
//		return err
//	}
//	defer f.Close()
//	res, err := c.Upload(ctx, peertube.UploadParams{
//		Name:      "My video",
//		ChannelID: 3,
//		Privacy:   peertube.PrivacyPublic,
//	}, "video.mp4", f)
//	if err != nil {
//		return err
//	}
//	fmt.Println("uploaded", res.UUID)
package peertube
