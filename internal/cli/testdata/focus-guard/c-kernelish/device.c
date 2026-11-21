#include "ops.h"

static int helper_impl(int value)
{
	return value + 1;
}

static int device_open(int fd)
{
	return CALL_HELPER(fd);
}

static int device_release(int fd)
{
	return helper_impl(fd);
}

static const struct file_operations device_fops = {
	.open = device_open,
	.release = device_release,
};

int device_register(const struct file_operations *ops)
{
	return ops->open(0);
}

int device_init(void)
{
	return device_register(&device_fops);
}
