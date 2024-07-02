# TVGIF

`tvgif` is a discord bot for creating gifs of TV shows by searching for a line of dialog.

To do this it requires you to provide `.webm` videos and corresponding `.srt` subtitles.

### DEMO

<img src="./tvgif-demo.gif" style="max-width: 800px">

### Query language

| Prefix | Field          | Example                 | Description                               |
|--------|----------------|-------------------------|-------------------------------------------|
| ~      | publication    | `~sunny`                | Filters subtitles by publication          |
| #      | series/episode | `#S1E04`, `#S1`, `#E04` | Filter by a series and/or episode number. |
| +      | timestamp      | `+1m`, `+10m30s`        | Filter by timestamp greater than.         |
| "      | content        | `"day man"`             | Phrase match                              |


#### Examples

* `day man` - search for any dialog containing `day` or `man` in any order/location.
* `"day man"` - search for any dialog containing the phrase `day man` in that order (case insensitive).
* `~sunny day` - search for any dialog from the `sunny` publication containing `day`
* `~sunny +1m30s #S3E09 man "day"` - search for dialog from the `sunny` publication, season 3 episode 9 occurring after `1m30s` and containing the word `man` and `day`.

### Controls

| Control                   | Description                                                                                 | 
|---------------------------|---------------------------------------------------------------------------------------------|
| ⏪ Next/Previous Subtitle  | Skip to the next/previous subtitle (chronologically). Note this will reset transformations. |
| ➕ Merge Next subtitle     | Add the next subtitle to the gif (up to 5)                                                  |
| ⏪ 5s, ⏪ 1s, etc.          | Shift the without changing the subtitles (e.g. to fix minor alignment issues)               | 
| ➕ 1s, ➕ 5s, etc.          | Extend the video without changing the subtitles.                                            | 
| ✂ 1s, ✂ 5s, etc.          | Trim the video (e.g. to cut off frame transition)                                           |
| ✂ Merged Subtitles        | If the gif contains multiple subtitles, this will trim all but the first.                   |
| Post GIF                  | Post the gif as seen in the preview.                                                        | 
| Post GIF with Custom Text | Alter the subtitle(s) before posting. Note no preview will be shown.                        |                    

Since the gif is posted by the bot you cannot delete it in the normal way. Instead, there is an app command to do it.
Right-click the post and go to `Apps -> tvgif-delete`. This will only work if you posted the original gif.


### Deploying with Docker

For example with docker compose:

```
version: "3.2"
services:
  server:
    image: "warmans/tvgif:latest"
    volumes:
      # Remember to chown shared dirs on host to nobody:nogroup so container can write to it
      - ${PWD}/media:/media       # location of webm/srts 
      - ${PWD}/cache:/cache       # cached gifs
      - ${PWD}/var:/opt/tvgif/var # metadata
    environment:
      DISCORD_TOKEN: [CHANGE ME]
      MEDIA_PATH: /media
      CACHE_PATH: /cache
    restart: unless-stopped
```

`webm`/`srt` files need to be added to the media dir. When the bot starts it will index the files.

Files use a specific naming convention e.g. `publication-S01E01.webm`. They must be `webm` format, and they must 
have `srt` files. Fortunately ffmpeg can convert almost anything to webm. There are example scripts in the `script` directory.

The workflow would be:

```bash
$ cd ~/workspace                            | you need a working directory to process files
$ cp -r ~/tvgif/script ./script             | copy the scripts from this repo
$ cp ~/my-tv-show/*.mp4 .                   | copy some mp4 files from somewhere
$ ./script/rename.sh mp4 myshow             | rename them to the correct format
$ ./script/extract-subs.sh mp4              | extract subs (where applicable)
$ ./script/resize-all.sh mp4                | transcode all mp4s to webm (resize, reduce framerate, strip audio)
$ cp *.webm ~/media/. && cp *.srt ~/media/. | copy webm and srt files to tvgif media dir
$ rm *.mp4                                  | delete the mp4s  
```
You would likely do this locally and then rsync the files to a server.

To allow the bot to run it needs a discord token. This can be obtained by creating a new bot: https://discord.com/developers/applications

The bot needs `applications.commands` and `bot` scopes.

Then use the Discord Provided Link to add the bot to a server/guild.
