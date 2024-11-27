FROM debian:stable-slim

RUN apt update && apt install -y gcc libfreetype-dev ffmpeg

RUN mkdir -p /opt/tvgif/var/metadata && RUN mkdir -p /opt/tvgif/var/assets && chown -R nobody /opt/tvgif

RUN addgroup nobody

ARG USER=nobody
USER nobody

WORKDIR /opt/tvgif

COPY --chown=nobody assets/* assets/.
COPY --chown=nobody bin/tvgif .

RUN chmod +x tvgif

CMD ["/opt/tvgif/tvgif", "bot"]
