FROM debian:stable-slim

RUN apt update && apt install -y gcc libfreetype-dev ffmpeg

RUN mkdir -p /opt/tvgif && chown -R nobody /opt/rsk

RUN addgroup nobody

ARG USER=nobody
USER nobody

WORKDIR /opt/tvgif

COPY --chown=nobody bin/tvgif .

RUN chmod +x tvgif

CMD ["/opt/tvgif/tvgif", "bot"]
