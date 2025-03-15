# Run server dependencies

local_resource(
    'tvgif-bot',
    dir='.',
    serve_dir='.',
    cmd='make build',
    serve_cmd='./bin/tvgif bot',
    serve_env={'DEV': 'true'},
    ignore=['./bin', './var', ".git"],
    deps='.',
    labels=['Bots'],
)
