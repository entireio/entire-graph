#include <stddef.h>

typedef struct Buffer { char *data; } Buffer;

Buffer *resize(Buffer *buffer, size_t want) {
    return grow(buffer, want);
}

Buffer *grow(Buffer *buffer, size_t want) {
    return buffer;
}
