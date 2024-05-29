# Run server dependencies

local_resource(
    'scrimpton-bot',
    dir='.',
    serve_dir='.',
    cmd='make build',
    serve_cmd='make run.discord-bot',
    ignore=['./bin', './var', ".git"],
    deps='.',
    labels=['Bots'],
    env={"DEBUG": "true"},
)
