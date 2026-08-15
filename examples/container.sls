# An OCI container through docker or podman, whichever the host has.
# Salt spells these docker_container.running and docker_image.present;
# halite names them for what they drive, since podman is what a FreeBSD
# host runs.
#
#   halite apply examples/container.sls -test

docker.io/library/nginx:1.27:
  container.image_present:
    - force: true

web:
  container.running:
    - image: docker.io/library/nginx:1.27
    - ports:
      - 8080:80
    - volumes:
      - /srv/site:/usr/share/nginx/html:ro
    - env:
        NGINX_HOST: example.com
        TZ: UTC
    - restart: always
    - require:
      - container: docker.io/library/nginx:1.27

# The arguments above are hashed into a label, so changing any of them —
# a port, an environment value, or the image the tag now resolves to —
# recreates the container on the next run. Nothing else has to be
# compared by hand.

old-api:
  container.absent:
