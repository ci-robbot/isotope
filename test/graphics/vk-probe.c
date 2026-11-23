/*
 * Create a Vulkan instance through the loader (libvulkan.so.1) and list the
 * physical devices, the first thing every Vulkan app and zink do. In a
 * container there is no GPU, so the one device must be lavapipe, Mesa's
 * Vulkan driver on the CPU, whose name starts with "llvmpipe". That proves the
 * loader, the ICD files in /usr/share/vulkan/icd.d and a Mesa Vulkan driver
 * work together.
 *
 * The loader is dlopened, not linked, and the few Vulkan types are declared
 * here, so the probe needs only libc to build (no Vulkan headers in the
 * toolchain) and runs against the loader of the image under test.
 *
 * Exit status: 0 when lavapipe is listed, 1 otherwise.
 */
#include <dlfcn.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

typedef int32_t VkResult;
typedef void *VkInstance;
typedef void *VkPhysicalDevice;
typedef void (*PFN_vkVoidFunction)(void);

#define VK_STRUCTURE_TYPE_APPLICATION_INFO 0
#define VK_STRUCTURE_TYPE_INSTANCE_CREATE_INFO 1
#define VK_API_VERSION_1_1 ((1u << 22) | (1u << 12))

typedef struct {
	uint32_t sType;
	const void *pNext;
	const char *pApplicationName;
	uint32_t applicationVersion;
	const char *pEngineName;
	uint32_t engineVersion;
	uint32_t apiVersion;
} VkApplicationInfo;

typedef struct {
	uint32_t sType;
	const void *pNext;
	uint32_t flags;
	const VkApplicationInfo *pApplicationInfo;
	uint32_t enabledLayerCount;
	const char *const *ppEnabledLayerNames;
	uint32_t enabledExtensionCount;
	const char *const *ppEnabledExtensionNames;
} VkInstanceCreateInfo;

/*
 * VkPhysicalDeviceProperties starts with five uint32_t (apiVersion,
 * driverVersion, vendorID, deviceID, deviceType) and then
 * deviceName[256]. The whole struct is under 1 KiB; the buffer is larger so
 * the driver never writes past it.
 */
#define DEVICE_NAME_OFFSET 20

typedef PFN_vkVoidFunction (*GetInstanceProcAddrFn)(VkInstance, const char *);
typedef VkResult (*CreateInstanceFn)(const VkInstanceCreateInfo *, const void *, VkInstance *);
typedef VkResult (*EnumeratePhysicalDevicesFn)(VkInstance, uint32_t *, VkPhysicalDevice *);
typedef void (*GetPhysicalDevicePropertiesFn)(VkPhysicalDevice, void *);
typedef void (*DestroyInstanceFn)(VkInstance, const void *);

int main(void)
{
	void *loader = dlopen("libvulkan.so.1", RTLD_NOW | RTLD_LOCAL);
	if (!loader) {
		fprintf(stderr, "vk-probe: %s\n", dlerror());
		return 1;
	}
	GetInstanceProcAddrFn gipa = (GetInstanceProcAddrFn)dlsym(loader, "vkGetInstanceProcAddr");
	if (!gipa) {
		fprintf(stderr, "vk-probe: no vkGetInstanceProcAddr in libvulkan.so.1\n");
		return 1;
	}
	CreateInstanceFn create = (CreateInstanceFn)gipa(NULL, "vkCreateInstance");
	if (!create) {
		fprintf(stderr, "vk-probe: no vkCreateInstance\n");
		return 1;
	}

	VkApplicationInfo app = {
		.sType = VK_STRUCTURE_TYPE_APPLICATION_INFO,
		.pApplicationName = "vk-probe",
		.apiVersion = VK_API_VERSION_1_1,
	};
	VkInstanceCreateInfo info = {
		.sType = VK_STRUCTURE_TYPE_INSTANCE_CREATE_INFO,
		.pApplicationInfo = &app,
	};
	VkInstance instance = NULL;
	VkResult r = create(&info, NULL, &instance);
	if (r != 0) {
		fprintf(stderr, "vk-probe: vkCreateInstance failed: %d\n", r);
		return 1;
	}

	EnumeratePhysicalDevicesFn enumerate =
		(EnumeratePhysicalDevicesFn)gipa(instance, "vkEnumeratePhysicalDevices");
	GetPhysicalDevicePropertiesFn props =
		(GetPhysicalDevicePropertiesFn)gipa(instance, "vkGetPhysicalDeviceProperties");
	DestroyInstanceFn destroy = (DestroyInstanceFn)gipa(instance, "vkDestroyInstance");
	if (!enumerate || !props || !destroy) {
		fprintf(stderr, "vk-probe: missing instance functions\n");
		return 1;
	}

	VkPhysicalDevice devs[16];
	uint32_t n = 16;
	r = enumerate(instance, &n, devs);
	if (r < 0) {
		fprintf(stderr, "vk-probe: vkEnumeratePhysicalDevices failed: %d\n", r);
		return 1;
	}
	int found = 0;
	for (uint32_t i = 0; i < n; i++) {
		_Alignas(16) unsigned char buf[4096];
		memset(buf, 0, sizeof(buf));
		props(devs[i], buf);
		const char *name = (const char *)buf + DEVICE_NAME_OFFSET;
		printf("device %u: %.255s\n", i, name);
		if (strncmp(name, "llvmpipe", 8) == 0)
			found = 1;
	}
	destroy(instance, NULL);
	if (!found) {
		fprintf(stderr, "vk-probe: lavapipe (llvmpipe) is not among %u devices\n", n);
		return 1;
	}
	return 0;
}
