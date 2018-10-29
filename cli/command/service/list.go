package service

import (
	"context"
	"fmt"
	"sort"

	"vbom.ml/util/sortorder"

	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/command/formatter"
	"github.com/docker/cli/opts"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
	"github.com/spf13/cobra"
)

type listOptions struct {
	quiet  bool
	format string
	filter opts.FilterOpt
}

func newListCommand(dockerCli command.Cli) *cobra.Command {
	options := listOptions{filter: opts.NewFilterOpt()}

	cmd := &cobra.Command{
		Use:     "ls [OPTIONS]",
		Aliases: []string{"list"},
		Short:   "List services",
		Args:    cli.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(dockerCli, options)
		},
	}

	flags := cmd.Flags()
	flags.BoolVarP(&options.quiet, "quiet", "q", false, "Only display IDs")
	flags.StringVar(&options.format, "format", "", "Pretty-print services using a Go template")
	flags.VarP(&options.filter, "filter", "f", "Filter output based on conditions provided")

	return cmd
}

func runList(dockerCli command.Cli, options listOptions) error {
	ctx := context.Background()
	client := dockerCli.Client()

	serviceFilters := options.filter.Value()
	services, err := client.ServiceList(ctx, types.ServiceListOptions{Filters: serviceFilters})
	if err != nil {
		return err
	}

	sort.Slice(services, func(i, j int) bool {
		return sortorder.NaturalLess(services[i].Spec.Name, services[j].Spec.Name)
	})
	info := map[string]formatter.ServiceListInfo{}
	if len(services) > 0 && !options.quiet {
		// only non-empty services and not quiet, should we call TaskList and NodeList api
		// we should get tasks individually, one-by-one. This takes longer (a
		// new request every time) but works around an issue where attempting
		// to list all tasks causes a gRPC message that's too big in the engine.
		//
		// NOTE(dperny): this can be reverted once a proper engine fix goes in,
		// but for now this band-aid should be fine.
		allTasks := []swarm.Task{}
		for _, service := range services {
			taskFilter := filters.NewArgs()
			taskFilter.Add("service", service.ID)

			tasks, err := client.TaskList(ctx, types.TaskListOptions{Filters: taskFilter})
			if err != nil {
				return err
			}
			allTasks = append(allTasks, tasks...)
		}

		nodes, err := client.NodeList(ctx, types.NodeListOptions{})
		if err != nil {
			return err
		}

		info = GetServicesStatus(services, nodes, allTasks)
	}

	format := options.format
	if len(format) == 0 {
		if len(dockerCli.ConfigFile().ServicesFormat) > 0 && !options.quiet {
			format = dockerCli.ConfigFile().ServicesFormat
		} else {
			format = formatter.TableFormatKey
		}
	}

	servicesCtx := formatter.Context{
		Output: dockerCli.Out(),
		Format: formatter.NewServiceListFormat(format, options.quiet),
	}
	return formatter.ServiceListWrite(servicesCtx, services, info)
}

// GetServicesStatus returns a map of mode and replicas
func GetServicesStatus(services []swarm.Service, nodes []swarm.Node, tasks []swarm.Task) map[string]formatter.ServiceListInfo {
	running := map[string]int{}
	tasksNoShutdown := map[string]int{}

	activeNodes := make(map[string]struct{})
	for _, n := range nodes {
		if n.Status.State != swarm.NodeStateDown {
			activeNodes[n.ID] = struct{}{}
		}
	}

	for _, task := range tasks {
		if task.DesiredState != swarm.TaskStateShutdown {
			tasksNoShutdown[task.ServiceID]++
		}

		if _, nodeActive := activeNodes[task.NodeID]; nodeActive && task.Status.State == swarm.TaskStateRunning {
			running[task.ServiceID]++
		}
	}

	info := map[string]formatter.ServiceListInfo{}
	for _, service := range services {
		info[service.ID] = formatter.ServiceListInfo{}
		if service.Spec.Mode.Replicated != nil && service.Spec.Mode.Replicated.Replicas != nil {
			info[service.ID] = formatter.ServiceListInfo{
				Mode:     "replicated",
				Replicas: fmt.Sprintf("%d/%d", running[service.ID], *service.Spec.Mode.Replicated.Replicas),
			}
		} else if service.Spec.Mode.Global != nil {
			info[service.ID] = formatter.ServiceListInfo{
				Mode:     "global",
				Replicas: fmt.Sprintf("%d/%d", running[service.ID], tasksNoShutdown[service.ID]),
			}
		}
	}
	return info
}
