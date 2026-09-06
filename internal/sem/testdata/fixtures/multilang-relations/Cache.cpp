#include <string>

struct Entry { std::string key; };

Entry lookup(Entry probe) {
    return fetch(probe);
}

Entry fetch(Entry probe) {
    return probe;
}
