build-kawata:
	rm -rf ./dist/*
	./build
install:
	cp ./dist/cap-* ~/.local/bin/cap
build-install:
	rm -rf ./dist/*
	./build
	cp ./dist/cap-* ~/.local/bin/cap