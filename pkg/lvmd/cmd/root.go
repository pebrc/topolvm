package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/cybozu-go/topolvm"
	"github.com/cybozu-go/topolvm/lvmd"
	"github.com/cybozu-go/topolvm/lvmd/command"
	"github.com/cybozu-go/topolvm/lvmd/proto"
	"github.com/cybozu-go/well"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

var config struct {
	vgName     string
	socketName string
	spareGB    uint64
}

// DefaultSocketName defines the default UNIX domain socket path.
const (
	DefaultSocketName = "/run/topolvm/lvmd.sock"
	maxDevNameLength  = 127
	k8sUIDLength      = 36
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:     "lvmd",
	Version: topolvm.Version,
	Short:   "a gRPC service to manage LVM volumes",
	Long: `A gRPC service to manage LVM volumes.

lvmd handles a LVM volume group and provides gRPC API to manage logical
volumes in the volume group.

If command-line option "spare" is not zero, that value multiplied by 1 GiB
will be subtracted from the value lvmd reports as the free space of the
volume group.
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return subMain()
	},
}

func subMain() error {
	err := well.LogConfig{}.Apply()
	if err != nil {
		return err
	}

	// the limit of the lv name length is 127 including vgname and 3.
	// https://github.com/lvmteam/lvm2/blob/24bd35b4ce77a5111ee234f554ca139df4b7dd99/lib/metadata/metadata.c#L2386
	if len(config.vgName) > (maxDevNameLength - k8sUIDLength - 3) {
		return errors.New("volume-group is too long")
	}
	vg, err := command.FindVolumeGroup(config.vgName)
	if err != nil {
		return err
	}

	// UNIX domain socket file should be removed before listening.
	err = os.Remove(config.socketName)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	lis, err := net.Listen("unix", config.socketName)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer()
	vgService, notifier := lvmd.NewVGService(vg, config.spareGB)
	proto.RegisterVGServiceServer(grpcServer, vgService)
	proto.RegisterLVServiceServer(grpcServer, lvmd.NewLVService(vg, notifier))
	well.Go(func(ctx context.Context) error {
		return grpcServer.Serve(lis)
	})
	well.Go(func(ctx context.Context) error {
		<-ctx.Done()
		grpcServer.GracefulStop()
		return nil
	})
	well.Go(func(ctx context.Context) error {
		ticker := time.NewTicker(10 * time.Minute)
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return nil
			case <-ticker.C:
				notifier()
			}
		}
	})
	err = well.Wait()
	if err != nil && !well.IsSignaled(err) {
		return err
	}
	return nil
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().StringVar(&config.vgName, "volume-group", "", "LVM volume group name")
	rootCmd.Flags().StringVar(&config.socketName, "listen", DefaultSocketName, "Unix domain socket name")
	rootCmd.Flags().Uint64Var(&config.spareGB, "spare", 10, "storage capacity in GiB to be spared")
}
