# TVGIF

`tvgif` is a discord bot for creating gifs of TV shows by searching for a line of dialog.

To do this it requires you to provide `.webm` videos and corresponding `.srt` subtitles.

### DEMO

<img src="./tvgif-demo.gif" style="max-width: 800px">

### Query language

See the options available for queries here: [Queries](pkg/docs/topics/Queries.md)

### Controls

See how to use the gifs controls here: [Controls](pkg/docs/topics/Controls.md)

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
