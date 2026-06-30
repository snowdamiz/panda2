package youtube

const (
	youtubeYTDLPUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	youtubeYTDLPPlayerClients = "default,ios"
	youtubeClipSourceFormat   = "bestvideo[height<=1080][vcodec^=avc1][ext=mp4]+bestaudio[ext=m4a]/bestvideo[height<=1080][ext=mp4]+bestaudio[ext=m4a]/bestvideo[height<=1080]+bestaudio/best[height<=1080]/best"
)

func youtubeYTDLPBaseArgs(extra ...string) []string {
	args := []string{
		"--no-warnings",
		"--no-cache-dir",
		"--user-agent", youtubeYTDLPUserAgent,
		"--referer", "https://www.youtube.com/",
		"--add-headers", "Accept-Language:en-US,en;q=0.9",
		"--extractor-args", "youtube:player_client=" + youtubeYTDLPPlayerClients,
		"--retries", "3",
		"--fragment-retries", "3",
		"--retry-sleep", "exp=1:20",
		"--extractor-retries", "3",
	}
	return append(args, extra...)
}

func youtubeYTDLPDownloadArgs(extra ...string) []string {
	return youtubeYTDLPBaseArgs(append([]string{
		"--no-playlist",
		"--no-progress",
	}, extra...)...)
}
