#ifndef OPS_H
#define OPS_H

struct file_operations {
	int (*open)(int fd);
	int (*release)(int fd);
};

#define CALL_HELPER(x) helper_impl(x)

int device_register(const struct file_operations *ops);

#endif
